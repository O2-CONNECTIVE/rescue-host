package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrRootConfig      = errors.New("固定配置rootが未設定です")
	ErrRootDenied      = errors.New("固定配置rootの保護を確認できません")
	ErrRootUnconfirmed = errors.New("固定配置rootの回収を確認できません")
	ErrRootUnavailable = errors.New("固定配置rootのnative readerを利用できません")
	ErrRootFatal       = bootstrapauth.ErrFatal
)

// AnchorはOS保護配置の固定入力。UI/env/argvから補完してはならない。
type Anchor struct {
	ServiceName, RecordPath, DigestPath string
	Volume                              protectedkey.VolumeProfile
	ReadTimeout, CleanupTimeout         time.Duration
}
type Subject struct{ ProcessUserSID, ServiceSID, ServiceName string }

type rootGroup struct {
	sid        string
	attributes uint32
}
type rootPrincipal struct {
	user                                  string
	kind                                  uint32
	tokenID, authenticationID, modifiedID uint64
	groups                                []rootGroup
	threadToken                           bool
}
type rootFileState struct {
	path, owner, filesystem                   string
	volume                                    uint64
	id                                        [16]byte
	acl                                       []protectedkey.ACE
	disk, directory, reparse, dacl, protected bool
	links                                     uint32
	size                                      int64
}
type rootToken interface {
	value() uintptr
	close() error
}
type rootFile interface {
	inspect() (rootFileState, error)
	read([]byte) (rootOperation, error)
	close() error
}
type rootKernel interface {
	threadID() uint32
	serviceSID(string) (string, error)
	principal(uintptr) (rootPrincipal, error)
	openQueryToken() (rootToken, error)
	open(string, bool) (rootFile, error)
}
type rootHeld struct {
	file   rootFile
	path   string
	leaf   bool
	before rootFileState
	op     *rootOwnedOperation
}
type Root struct {
	anchor     Anchor
	configured bool
	api        rootKernel
}

// Loadedは元callerが固定したOS threadの所有物。初回利用後にコピーしない。
// partial/unknownでも同じLoadedを保持し、同じCloseで回収する。
type Loaded struct {
	anchor                     Anchor
	api                        rootKernel
	thread                     uint32
	identity                   rootPrincipal
	subject                    Subject
	token                      rootToken
	held                       []*rootHeld
	record                     []byte
	gate                       sync.Mutex
	accepted, closed, poisoned atomic.Bool
}

func NewNativeRoot(a Anchor) (*Root, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return &Root{anchor: a, configured: true}, nil
}
func rootFinite(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
func rootLive(ctx context.Context) bool { return rootFinite(ctx) && ctx.Err() == nil }
func (r *Root) Open(ctx context.Context) (loaded *Loaded, result error) {
	if r == nil || !r.configured {
		return nil, ErrRootConfig
	}
	if !rootLive(ctx) {
		return nil, ErrRootDenied
	}
	api := r.api
	if api == nil {
		api = rootPlatform()
	}
	if api == nil {
		return nil, ErrRootUnavailable
	}
	deadline, _ := ctx.Deadline()
	if end := time.Now().Add(r.anchor.ReadTimeout); end.Before(deadline) {
		deadline = end
	}
	readCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	l := &Loaded{anchor: r.anchor, api: api, thread: api.threadID()}
	loaded = l
	defer func() {
		if result != nil {
			l.accepted.Store(false)
		}
		if errors.Is(result, ErrRootFatal) || errors.Is(result, protectedkey.ErrFatal) {
			l.poisoned.Store(true)
		}
	}()
	if l.thread == 0 {
		return l, ErrRootFatal
	}
	initial, err := api.principal(0)
	if err != nil || initial.threadToken || initial.kind != 1 || initial.tokenID == 0 || !rootSID(initial.user) {
		return l, errors.Join(ErrRootFatal, err)
	}
	l.identity = rootClonePrincipal(initial)
	l.subject = Subject{ProcessUserSID: initial.user, ServiceName: r.anchor.ServiceName}
	if !rootLive(readCtx) {
		return l, ErrRootDenied
	}
	derived, err := api.serviceSID(r.anchor.ServiceName)
	if err != nil || !rootServiceIdentifier(derived) {
		return l, errors.Join(ErrRootDenied, err)
	}
	if !rootHasServiceGroup(initial, derived) {
		return l, ErrRootDenied
	}
	l.subject.ServiceSID = derived
	if err = l.checkSubjectWithin(readCtx); err != nil {
		return l, err
	}
	if !rootLive(readCtx) {
		return l, ErrRootDenied
	}
	l.token, err = api.openQueryToken()
	if err != nil || l.token == nil {
		return l, errors.Join(ErrRootDenied, err)
	}
	if err = l.checkSubjectWithin(readCtx); err != nil {
		return l, err
	}
	paths := rootPaths(r.anchor.RecordPath)
	for _, p := range rootPaths(r.anchor.DigestPath) {
		found := false
		for _, q := range paths {
			if p == q {
				found = true
				break
			}
		}
		if !found {
			paths = append(paths, p)
		}
	}
	var record, digest *rootHeld
	for _, path := range paths {
		if !rootLive(readCtx) {
			return l, ErrRootDenied
		}
		if err = l.checkSubjectWithin(readCtx); err != nil {
			return l, err
		}
		leaf := path == r.anchor.RecordPath || path == r.anchor.DigestPath
		file, err := api.open(path, leaf)
		h := &rootHeld{file: file, path: path, leaf: leaf}
		if file != nil {
			l.held = append(l.held, h)
		}
		if err != nil || file == nil {
			return l, errors.Join(ErrRootDenied, err)
		}
		if !rootLive(readCtx) {
			return l, ErrRootDenied
		}
		state, err := file.inspect()
		if err != nil || !l.validFile(state, path, leaf) {
			return l, errors.Join(ErrRootDenied, err)
		}
		h.before = rootCloneState(state)
		if path == r.anchor.RecordPath {
			record = h
		}
		if path == r.anchor.DigestPath {
			digest = h
		}
	}
	if record == nil || digest == nil || record.before.id == digest.before.id || record.before.size < 1 || record.before.size > 256*1024 || digest.before.size != 65 {
		return l, ErrRootDenied
	}
	data, err := l.readFile(readCtx, record)
	if err != nil {
		return l, err
	}
	if err = l.recheckHeld(readCtx); err != nil {
		return l, err
	}
	checksum, err := l.readFile(readCtx, digest)
	if err != nil {
		return l, err
	}
	if err = l.recheckHeld(readCtx); err != nil {
		return l, err
	}
	if len(checksum) != 65 || checksum[64] != '\n' {
		return l, ErrRootDenied
	}
	decoded, err := hex.DecodeString(string(checksum[:64]))
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != string(checksum[:64]) {
		return l, ErrRootDenied
	}
	hash := sha256.Sum256(data)
	if !reflect.DeepEqual(decoded, hash[:]) {
		return l, ErrRootDenied
	}
	if err = l.checkSubjectWithin(readCtx); err != nil {
		return l, err
	}
	if !rootLive(readCtx) {
		return l, ErrRootDenied
	}
	l.record = append([]byte(nil), data...)
	l.accepted.Store(true)
	return l, nil
}
func (l *Loaded) checkSubject() error { return l.checkSubjectWithin(nil) }
func (l *Loaded) checkSubjectWithin(ctx context.Context) error {
	if ctx != nil && !rootLive(ctx) {
		return ErrRootDenied
	}
	if l.poisoned.Load() || l.thread == 0 || l.api.threadID() != l.thread {
		return ErrRootFatal
	}
	actual, err := l.api.principal(0)
	if err != nil || actual.threadToken || actual.kind != 1 || !reflect.DeepEqual(actual, l.identity) {
		return errors.Join(ErrRootFatal, err)
	}
	if l.subject.ServiceSID != "" && !rootHasServiceGroup(actual, l.subject.ServiceSID) {
		return ErrRootFatal
	}
	if l.token != nil {
		if ctx != nil && !rootLive(ctx) {
			return ErrRootDenied
		}
		held, err := l.api.principal(l.token.value())
		if err != nil || !reflect.DeepEqual(actual, held) {
			return errors.Join(ErrRootFatal, err)
		}
	}
	return nil
}
func (l *Loaded) validFile(s rootFileState, path string, leaf bool) bool {
	return s.disk && !s.reparse && s.dacl && s.path == path && s.volume == l.anchor.Volume.Serial && s.filesystem == l.anchor.Volume.Filesystem && s.id != [16]byte{} && s.directory != leaf && (!leaf || s.links == 1) && rootTrusted(l.subject.ServiceSID, s.owner) && rootSafeACL(l.subject.ServiceSID, s.acl)
}
func rootCloneState(s rootFileState) rootFileState {
	s.acl = append([]protectedkey.ACE(nil), s.acl...)
	return s
}
func (l *Loaded) recheckHeld(ctx context.Context) error {
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	if err := l.checkSubjectWithin(ctx); err != nil {
		return err
	}
	for _, h := range l.held {
		if !rootLive(ctx) {
			return ErrRootDenied
		}
		state, err := h.file.inspect()
		if err != nil || !l.validFile(state, h.path, h.leaf) || !reflect.DeepEqual(state, h.before) {
			return errors.Join(ErrRootDenied, err)
		}
	}
	return l.checkSubjectWithin(ctx)
}
func (l *Loaded) get() error {
	if l == nil || l.api == nil {
		return ErrRootDenied
	}
	if l.poisoned.Load() {
		return ErrRootFatal
	}
	if l.closed.Load() || !l.accepted.Load() {
		return ErrRootDenied
	}
	if l.api.threadID() != l.thread {
		l.poisoned.Store(true)
		l.accepted.Store(false)
		return ErrRootFatal
	}
	if !l.gate.TryLock() {
		return ErrRootDenied
	}
	if l.closed.Load() || !l.accepted.Load() {
		l.gate.Unlock()
		return ErrRootDenied
	}
	if err := l.checkSubject(); err != nil {
		l.accepted.Store(false)
		l.poisoned.Store(true)
		l.gate.Unlock()
		return errors.Join(ErrRootFatal, err)
	}
	if l.poisoned.Load() {
		l.gate.Unlock()
		return ErrRootFatal
	}
	if l.closed.Load() || !l.accepted.Load() {
		l.gate.Unlock()
		return ErrRootDenied
	}
	return nil
}
func (l *Loaded) Record() ([]byte, error) {
	if err := l.get(); err != nil {
		return nil, err
	}
	defer l.gate.Unlock()
	return append([]byte(nil), l.record...), nil
}
func (l *Loaded) Subject() (Subject, error) {
	if err := l.get(); err != nil {
		return Subject{}, err
	}
	defer l.gate.Unlock()
	return l.subject, nil
}

// QueryTokenは照合専用のTOKEN_QUERY借用。launch handleや認可proofではない。
func (l *Loaded) QueryToken() (uintptr, error) {
	if err := l.get(); err != nil {
		return 0, err
	}
	defer l.gate.Unlock()
	if l.token == nil {
		return 0, ErrRootDenied
	}
	return l.token.value(), nil
}
func (l *Loaded) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.accepted.Store(false)
	l.closed.Store(true)
	if l.api != nil && l.thread != 0 && l.api.threadID() != l.thread {
		l.poisoned.Store(true)
	}
	if !rootFinite(ctx) {
		return l.finishClose(errors.Join(ErrRootConfig, ErrRootUnconfirmed))
	}
	deadline, _ := ctx.Deadline()
	if end := time.Now().Add(l.anchor.CleanupTimeout); end.Before(deadline) {
		deadline = end
	}
	cleanup, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if !rootAdmit(cleanup, &l.gate) {
		return l.finishClose(ErrRootUnconfirmed)
	}
	defer l.gate.Unlock()
	if !l.poisoned.Load() && rootLive(cleanup) && l.identity.tokenID != 0 {
		if err := l.checkSubjectWithin(cleanup); errors.Is(err, ErrRootFatal) {
			l.poisoned.Store(true)
		}
	}
	clear(l.record)
	l.record = nil
	var result error
	// 期限済みでも既pending requestへ一度のCancel/GetOR確認は行う。
	// 新しいsubject query/open/read/closeはfresh cleanup contextへ残す。
	for _, h := range l.held {
		if h.op != nil && !h.op.released.Load() {
			_, _, _ = h.op.tryStep(true)
		}
	}
	if !rootLive(cleanup) {
		if l.token != nil {
			return l.finishClose(ErrRootUnconfirmed)
		}
		for _, h := range l.held {
			if h.file != nil {
				return l.finishClose(ErrRootUnconfirmed)
			}
		}
		return l.finishClose(nil)
	}
	if l.token != nil {
		if err := l.token.close(); err != nil {
			result = errors.Join(result, ErrRootUnconfirmed, err)
		} else {
			l.token = nil
		}
	}
	for i := len(l.held) - 1; i >= 0; i-- {
		h := l.held[i]
		if h.file != nil && !rootLive(cleanup) {
			result = errors.Join(result, ErrRootUnconfirmed)
			continue
		}
		if h.file == nil {
			continue
		}
		if h.op != nil {
			for {
				done, _, err := h.op.tryStep(true)
				if done {
					break
				}
				if cleanup.Err() != nil || !time.Now().Before(deadline) {
					result = errors.Join(result, ErrRootUnconfirmed, err)
					break
				}
				rootPause(cleanup)
			}
			if !h.op.released.Load() {
				continue
			}
		}
		if !rootLive(cleanup) {
			result = errors.Join(result, ErrRootUnconfirmed)
			continue
		}
		if err := h.file.close(); err != nil {
			result = errors.Join(result, ErrRootUnconfirmed, err)
		} else {
			h.file = nil
		}
	}
	return l.finishClose(result)
}
func (l *Loaded) finishClose(err error) error {
	if errors.Is(err, ErrRootFatal) || errors.Is(err, protectedkey.ErrFatal) {
		l.poisoned.Store(true)
	}
	if l.poisoned.Load() {
		return errors.Join(ErrRootFatal, err)
	}
	return err
}
func rootAdmit(ctx context.Context, mu *sync.Mutex) bool {
	for {
		if mu.TryLock() {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		deadline, _ := ctx.Deadline()
		if !time.Now().Before(deadline) {
			return false
		}
		rootPause(ctx)
	}
}
func rootPause(ctx context.Context) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func rootHasServiceGroup(p rootPrincipal, serviceSID string) bool {
	for _, g := range p.groups {
		if g.sid == serviceSID && g.attributes&4 != 0 && g.attributes&0x10 == 0 {
			return true
		}
	}
	return false
}

func rootClonePrincipal(p rootPrincipal) rootPrincipal {
	p.groups = append([]rootGroup(nil), p.groups...)
	return p
}
