package guestaccount

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

type stagingScope interface {
	Recheck(context.Context) error
	Close(context.Context) error
}
type stagingResource interface {
	value() uintptr
	close(context.Context) error
}

// private OS境界。native factory以外へこの差替えを公開しない。
type stagingOps struct {
	thread    func() uint32
	principal func() (accountQueryPrincipal, error)
	scope     func(context.Context) (stagingScope, error)
	open      func(context.Context, permissionObjectKind, stagingFile, func() bool) (stagingResource, error)
	inspect   func(stagingResource, func() bool) (journalFileInfo, error)
	launch    func(context.Context, accountQueryPrincipal, func() bool) error
	journal   func(context.Context, Plan, *journalRootCapability) (*journalBackend, error)
	accounts  func(Plan, *seal, permissionProfile) (*creationSource, error)
}
type stagingHeld struct {
	ref  stagingResource
	file stagingFile
	kind permissionObjectKind
}

// 元service owner threadが唯一操作する。query workerはraw readonly handleを
// 借りるだけで、thread専用scopeやjournalを呼ばない。全ack前に借用を解かない。
type stagingOwner struct {
	self                   *stagingOwner
	mu                     sync.Mutex
	stop                   atomic.Bool
	plan                   Plan
	profile                stagingProfile
	ops                    stagingOps
	thread                 uint32
	identity               accountQueryPrincipal
	scope                  stagingScope
	held                   []stagingHeld
	journal                *journalBackend
	accounts               *creationSource
	lifecycle              *Lifecycle
	fatal, closed, blocked bool
}

func newStaging(ctx context.Context, p Plan, c stagingProfile, session sessionSource, sessionSeal *seal, ops stagingOps) (owner *stagingOwner, result error) {
	c, e := newStagingProfile(p, c)
	if e != nil || session == nil || sessionSeal == nil || !finite(ctx) || ops.thread == nil || ops.principal == nil || ops.scope == nil || ops.open == nil || ops.inspect == nil || ops.launch == nil || ops.journal == nil || ops.accounts == nil {
		return nil, ErrUnconfigured
	}
	o := &stagingOwner{plan: p, profile: c, ops: ops}
	o.self = o
	owner = o
	defer o.finish(&result)
	o.thread = ops.thread()
	o.identity, e = ops.principal()
	if e != nil || o.thread == 0 || !o.primary(o.identity) {
		o.fatal = true
		return o, bootstrapauth.ErrFatal
	}
	o.scope, e = ops.scope(ctx)
	if e != nil || o.scope == nil {
		return o, o.failure(e)
	}
	if e = o.recheck(ctx); e != nil {
		return o, e
	}
	for _, kind := range []permissionObjectKind{permissionObjectService, permissionObjectProcess, permissionObjectToken, permissionObjectPipe} {
		if e = o.acquire(ctx, kind, stagingFile{}); e != nil {
			return o, e
		}
	}
	// ancestorを先に保持し、ledgerはshare=0の既存journalだけが開く。
	for _, directory := range []bool{true, false} {
		for _, f := range c.files {
			if f.role == roleJournal || f.directory != directory {
				continue
			}
			kind := permissionObjectFile
			if directory {
				kind = permissionObjectDirectory
			}
			if e = o.acquire(ctx, kind, f); e != nil {
				return o, e
			}
		}
	}
	root := &journalRootCapability{installation: c.installation, machine: p.binding.machine, service: p.binding.service, serviceSID: c.serviceSID, thread: o.thread}
	root.self = root
	for _, h := range o.held {
		if h.file.path != "" && strings.EqualFold(h.file.path, c.journalRoot) {
			root.root = h.ref.value()
			root.rootID = h.file.identity
		}
	}
	for _, f := range c.files {
		if f.role == roleJournal {
			root.ledgerID = f.identity
		}
	}
	if !o.rawScope() || !finite(ctx) {
		return o, ErrDenied
	}
	o.journal, e = ops.journal(ctx, p, root)
	if e != nil || o.journal == nil {
		return o, o.failure(e)
	}
	if o.journal.count != 0 && !journalTerminal(o.journal.record.stage) {
		return o, ErrManual
	}
	if e = o.recheck(ctx); e != nil {
		return o, e
	}
	profile := permissionProfile{serviceSID: c.serviceSID, objects: []permissionObject{}}
	for _, h := range o.held {
		object := permissionObject{kind: h.kind, handle: h.ref.value(), role: h.file.role, fileID: h.file.identity.fileID, volume: h.file.identity.volume}
		switch h.kind {
		case permissionObjectService:
			object.role = roleService
		case permissionObjectProcess:
			object.role = roleSupervisorProcess
		case permissionObjectToken:
			object.role = roleSupervisorToken
			object.userSID = p.binding.service
		case permissionObjectPipe:
			object.role = roleControlPipe
		}
		profile.objects = append(profile.objects, object)
	}
	profile.objects = append(profile.objects, permissionObject{kind: permissionObjectFile, role: roleJournal, handle: o.journal.file, fileID: root.ledgerID.fileID, volume: root.ledgerID.volume})
	seal := &seal{8}
	o.accounts, e = ops.accounts(p, seal, profile)
	if e != nil || o.accounts == nil {
		return o, o.failure(e)
	}
	o.lifecycle, e = newLifecycle(p, sources{journal: o.journal, accounts: o.accounts, session: session, journalSeal: o.journal.seal, accountSeal: seal, sessionSeal: sessionSeal})
	if e != nil {
		return o, e
	}
	return o, o.recheck(ctx)
}

func (o *stagingOwner) primary(p accountQueryPrincipal) bool {
	return p.user == o.plan.binding.service && p.kind == 1 && !p.threadToken && !p.restricted && !p.appContainer && p.tokenID != 0 && p.authenticationID != 0 && p.modifiedID != 0
}
func (o *stagingOwner) rawScope() (ok bool) {
	defer func() {
		if recover() != nil {
			o.fatal = true
			ok = false
		}
	}()
	if o.fatal || o.ops.thread() != o.thread {
		o.fatal = true
		return false
	}
	p, e := o.ops.principal()
	if e != nil || p != o.identity || !o.primary(p) || o.ops.thread() != o.thread {
		o.fatal = true
		return false
	}
	return true
}
func (o *stagingOwner) failure(e error) error {
	if errors.Is(e, bootstrapauth.ErrFatal) {
		o.fatal = true
		return bootstrapauth.ErrFatal
	}
	if e == nil {
		return ErrUnknown
	}
	return e
}
func (o *stagingOwner) finish(result *error) {
	if recover() != nil {
		o.fatal = true
		*result = bootstrapauth.ErrFatal
	}
	if o.fatal {
		*result = bootstrapauth.ErrFatal
		return
	}
	// 借用queryのunknownと元ownerのdirtyは区別し、いずれも新nativeを再開しない。
	if o.blocked {
		return
	}
	if !o.rawScope() {
		*result = bootstrapauth.ErrFatal
	}
	if errors.Is(*result, bootstrapauth.ErrFatal) {
		o.fatal = true
	}
}
func (o *stagingOwner) recheck(ctx context.Context) error {
	if !o.rawScope() {
		return bootstrapauth.ErrFatal
	}
	if !finite(ctx) || o.stop.Load() {
		return ErrDenied
	}
	if e := o.scope.Recheck(ctx); e != nil {
		return o.failure(e)
	}
	if !o.rawScope() {
		return bootstrapauth.ErrFatal
	}
	if !finite(ctx) {
		return ErrDenied
	}
	if e := o.ops.launch(ctx, o.identity, o.rawScope); e != nil {
		return o.failure(e)
	}
	if !o.rawScope() {
		return bootstrapauth.ErrFatal
	}
	if !finite(ctx) {
		return ErrDenied
	}
	return nil
}
func (o *stagingOwner) acquire(ctx context.Context, kind permissionObjectKind, file stagingFile) error {
	if !o.rawScope() {
		return bootstrapauth.ErrFatal
	}
	if !finite(ctx) {
		return ErrDenied
	}
	ref, e := o.ops.open(ctx, kind, file, o.rawScope)
	if ref != nil {
		o.held = append(o.held, stagingHeld{ref: ref, file: file, kind: kind})
	}
	if !o.rawScope() {
		return bootstrapauth.ErrFatal
	}
	if e != nil || ref == nil || !finite(ctx) || !realJournalHandle(ref.value()) {
		return o.failure(e)
	}
	if file.path != "" {
		info, e := o.ops.inspect(ref, o.rawScope)
		if !o.rawScope() {
			return bootstrapauth.ErrFatal
		}
		if e != nil || info.identity != file.identity || info.kind != 1 || info.directory != file.directory || info.reparse || info.inherit || !info.protected || !file.directory && info.links != 1 {
			return ErrDenied
		}
	}
	return nil
}
func (o *stagingOwner) Prepare(ctx context.Context) (result error) {
	if o == nil || o.self != o || !finite(ctx) || !o.mu.TryLock() {
		return ErrDenied
	}
	defer o.mu.Unlock()
	defer o.finish(&result)
	if o.closed || o.lifecycle == nil {
		return ErrUnconfigured
	}
	if o.blocked {
		return ErrUnknown
	}
	if e := o.recheck(ctx); e != nil {
		return e
	}
	result = o.lifecycle.Prepare(ctx)
	if stagingQueryUnsafe(o.accounts) {
		o.blocked = true
		return ErrUnknown
	}
	if e := o.recheck(ctx); e != nil {
		return e
	}
	return result
}

// shutdown時は新規準備を拒否し、先に失効と実work回収を既存coreへ合流する。
// 不明/未終了queryならjournal/manifest/leaseを解放しない。
func (o *stagingOwner) Close(ctx context.Context) (result error) {
	if o == nil {
		return nil
	}
	if o.self != o {
		return ErrDenied
	}
	o.stop.Store(true)
	if o.lifecycle != nil {
		o.lifecycle.stop.Store(true)
	}
	if !finite(ctx) || !o.mu.TryLock() {
		return ErrCleanup
	}
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	defer o.finish(&result)
	if o.blocked {
		return ErrUnknown
	}
	if !o.rawScope() {
		return bootstrapauth.ErrFatal
	}
	if o.lifecycle != nil {
		result = o.lifecycle.Close(ctx)
	}
	if o.accounts != nil && stagingQueryUnsafe(o.accounts) {
		o.blocked = true
		return ErrUnknown
	}
	// 清掃が再試行可能な間はaccount sourceの永久stopを行わない。
	if result != nil || !finite(ctx) {
		return errors.Join(result, ErrCleanup)
	}
	if o.accounts != nil {
		result = errors.Join(result, o.accounts.Close(ctx))
	}
	if result != nil || !finite(ctx) {
		return errors.Join(result, ErrCleanup)
	}
	if o.journal != nil {
		if e := o.journal.close(ctx); e != nil {
			return e
		}
		o.journal = nil
	}
	for n := len(o.held) - 1; n >= 0; n-- {
		if !finite(ctx) || !o.rawScope() {
			return ErrCleanup
		}
		if e := o.held[n].ref.close(ctx); e != nil {
			return e
		}
		o.held = o.held[:n]
		if !o.rawScope() {
			return bootstrapauth.ErrFatal
		}
	}
	if o.scope != nil {
		if e := o.scope.Close(ctx); e != nil {
			return o.failure(e)
		}
		o.scope = nil
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	o.closed = true
	return nil
}
