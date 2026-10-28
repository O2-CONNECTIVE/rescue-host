package serviceprovision

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrLaunchConfig      = errors.New("launch tokenの固定保護設定が不足しています")
	ErrLaunchDenied      = errors.New("launch tokenの保護証拠を確認できません")
	ErrLaunchUnconfirmed = errors.New("launch token所有物の回収を確認できません")
	ErrLaunchFatal       = bootstrapauth.ErrFatal
)

type LaunchFactory struct {
	c          serviceready.Config
	ready      *serviceready.Factory
	configured bool
	api        launchKernel
}
type launchPreflight interface {
	Recheck(context.Context) error
	Binding(context.Context) (serviceready.ProcessBinding, error)
	Close(context.Context) error
}
type launchTokenRef interface {
	value() uintptr
	permissions() (uint32, uint32, error)
	protect() (launchProtection, error)
	close() error
}
type launchThreadRef interface {
	inspect() (launchThreadState, error)
	close() error
}
type launchSnapshot interface {
	threads(context.Context, uint32) ([]uint32, error)
	close() error
}
type launchKernel interface {
	threadID() uint32
	currentPID() uint32
	preflight(context.Context) (launchPreflight, error)
	identity(uintptr) (rootPrincipal, error)
	openToken(uint32) (launchTokenRef, error)
	snapshot() (launchSnapshot, error)
	openThread(uint32) (launchThreadRef, error)
}
type launchObjectSecurity struct {
	owner string
	acl   []protectedkey.ACE
}
type launchProtection struct {
	object       launchObjectSecurity
	defaultOwner string
	defaultACL   []protectedkey.ACE
}
type launchThreadState struct {
	pid, tid      uint32
	creation      uint64
	alive         bool
	flags, access uint32
	security      launchObjectSecurity
}
type launchHeldThread struct {
	ref    launchThreadRef
	before launchThreadState
}

// LaunchTokenは同じ元OS threadで保持し、初回利用後コピーしない。
// 借用中のClose防止/leaseは外側のexecution ownerが担う。
type LaunchToken struct {
	c                                   serviceready.Config
	api                                 launchKernel
	gate                                sync.Mutex
	pre                                 launchPreflight
	security, strong                    launchTokenRef
	snapshot                            launchSnapshot
	threads                             []launchHeldThread
	binding                             serviceready.ProcessBinding
	thread                              uint32
	identity                            rootPrincipal
	protection                          launchProtection
	accepted, revoked, closed, poisoned atomic.Bool
}

// NewNativeLaunchFactoryはpureな固定値検証と複製だけを行い、tokenを開かない。
func NewNativeLaunchFactory(c serviceready.Config) (*LaunchFactory, error) {
	ready, err := serviceready.NewNative(c)
	if err != nil {
		return nil, ErrLaunchConfig
	}
	c = launchCloneConfig(c)
	return &LaunchFactory{c: c, ready: ready, configured: true}, nil
}
func (f *LaunchFactory) Prepare(ctx context.Context) (owner *LaunchToken, result error) {
	if f == nil || !f.configured {
		return nil, ErrLaunchConfig
	}
	if !rootLive(ctx) {
		return nil, ErrLaunchDenied
	}
	api := f.api
	if api == nil {
		api = launchPlatform(f.ready)
	}
	if api == nil {
		return nil, ErrLaunchTokenUnconfigured
	}
	l := &LaunchToken{c: f.c, api: api, thread: api.threadID()}
	owner = l
	defer func() {
		if result != nil {
			l.fail(result)
		}
	}()
	if l.thread == 0 {
		return l, ErrLaunchFatal
	}
	l.pre, result = api.preflight(ctx)
	if result != nil || l.pre == nil {
		return l, errors.Join(ErrLaunchDenied, result)
	}
	l.binding, result = l.pre.Binding(ctx)
	if result != nil || l.binding.PID == 0 || l.binding.Creation == 0 || api.currentPID() != l.binding.PID {
		return l, errors.Join(ErrLaunchDenied, result)
	}
	identity, err := api.identity(0)
	if err != nil || !launchIdentityValid(f.c, identity) {
		return l, errors.Join(ErrLaunchFatal, err)
	}
	l.identity = rootClonePrincipal(identity)
	if err = l.subject(ctx); err != nil {
		return l, err
	}
	if !rootLive(ctx) {
		return l, ErrLaunchDenied
	}
	l.security, result = api.openToken(0x20008)
	if result != nil || l.security == nil {
		return l, errors.Join(ErrLaunchDenied, result)
	}
	if err = l.tokenIdentity(l.security, 0x20008); err != nil {
		return l, err
	}
	protection, err := l.security.protect()
	if err != nil || !launchProtectionSafe(f.c, protection) {
		return l, errors.Join(ErrLaunchDenied, err)
	}
	l.protection = launchCloneProtection(protection)
	ids, err := l.scanThreads(ctx)
	if err != nil {
		return l, err
	}
	for _, id := range ids {
		if !rootLive(ctx) {
			return l, ErrLaunchDenied
		}
		ref, err := api.openThread(id)
		if ref != nil {
			l.threads = append(l.threads, launchHeldThread{ref: ref})
		}
		if err != nil || ref == nil {
			return l, errors.Join(ErrLaunchDenied, err)
		}
		state, err := ref.inspect()
		if err != nil || !l.threadSafe(state, id) {
			return l, errors.Join(ErrLaunchDenied, err)
		}
		l.threads[len(l.threads)-1].before = launchCloneThread(state)
	}
	if err = l.verify(ctx, false); err != nil {
		return l, err
	}
	if !rootLive(ctx) {
		return l, ErrLaunchDenied
	}
	l.strong, result = api.openToken(0xb)
	if result != nil || l.strong == nil {
		return l, errors.Join(ErrLaunchDenied, result)
	}
	if err = l.verify(ctx, true); err != nil {
		return l, err
	}
	l.accepted.Store(true)
	return l, nil
}
func (l *LaunchToken) fail(err error) error {
	l.revoked.Store(true)
	if errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal) {
		l.poisoned.Store(true)
	}
	return err
}
func launchIdentityValid(c serviceready.Config, p rootPrincipal) bool {
	return !p.threadToken && p.kind == 1 && p.tokenID != 0 && p.user == c.Profile.Keys.ProcessUserSID && rootHasServiceGroup(p, c.Profile.Keys.ServiceSID)
}
func (l *LaunchToken) subject(ctx context.Context) error {
	if l.poisoned.Load() || l.thread == 0 || l.api.threadID() != l.thread {
		return ErrLaunchFatal
	}
	if !rootLive(ctx) {
		return ErrLaunchDenied
	}
	current, err := l.api.identity(0)
	if err != nil || !launchIdentityValid(l.c, current) || !reflect.DeepEqual(current, l.identity) {
		return errors.Join(ErrLaunchFatal, err)
	}
	if !rootLive(ctx) {
		return ErrLaunchDenied
	}
	borrowed, err := l.api.identity(l.c.PrimaryToken)
	if err != nil || !reflect.DeepEqual(borrowed, l.identity) {
		return errors.Join(ErrLaunchFatal, err)
	}
	return nil
}
func (l *LaunchToken) tokenIdentity(ref launchTokenRef, expected uint32) error {
	if ref == nil || ref.value() == 0 || ref.value() >= ^uintptr(0)-15 {
		return ErrLaunchDenied
	}
	access, flags, err := ref.permissions()
	if err != nil || access != expected || flags != 0 {
		return errors.Join(ErrLaunchDenied, err)
	}
	actual, err := l.api.identity(ref.value())
	if err != nil || !reflect.DeepEqual(actual, l.identity) {
		return errors.Join(ErrLaunchFatal, err)
	}
	return nil
}

const launchMaxThreads = 1024
const launchMaxSystemThreads = 65536

func (l *LaunchToken) scanThreads(ctx context.Context) ([]uint32, error) {
	if !rootLive(ctx) || l.snapshot != nil {
		return nil, ErrLaunchDenied
	}
	snapshot, err := l.api.snapshot()
	if snapshot != nil {
		l.snapshot = snapshot
	}
	if err != nil || snapshot == nil {
		return nil, errors.Join(ErrLaunchDenied, err)
	}
	ids, err := snapshot.threads(ctx, l.binding.PID)
	if err != nil {
		return nil, errors.Join(ErrLaunchDenied, err)
	}
	if len(ids) == 0 || len(ids) > launchMaxThreads {
		return nil, ErrLaunchDenied
	}
	ids = append([]uint32(nil), ids...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	caller := false
	for i, id := range ids {
		if id == 0 || i > 0 && id == ids[i-1] {
			return nil, ErrLaunchDenied
		}
		if id == l.thread {
			caller = true
		}
	}
	if !caller || !rootLive(ctx) {
		return nil, ErrLaunchDenied
	}
	if err = snapshot.close(); err != nil {
		return nil, errors.Join(ErrLaunchUnconfirmed, err)
	}
	l.snapshot = nil
	return ids, nil
}
func (l *LaunchToken) threadSafe(s launchThreadState, tid uint32) bool {
	return s.pid == l.binding.PID && s.tid == tid && s.creation != 0 && s.alive && s.flags == 0 && s.access == 0x120800 && launchTrusted(l.c, s.security.owner) && launchACL(l.c, s.security.acl, 1)
}
func (l *LaunchToken) verify(ctx context.Context, strong bool) error {
	if err := l.subject(ctx); err != nil {
		return err
	}
	if err := l.pre.Recheck(ctx); err != nil {
		return err
	}
	binding, err := l.pre.Binding(ctx)
	if err != nil || binding != l.binding || l.api.currentPID() != l.binding.PID {
		return errors.Join(ErrLaunchDenied, err)
	}
	if l.security == nil {
		return ErrLaunchDenied
	}
	if err := l.tokenIdentity(l.security, 0x20008); err != nil {
		return err
	}
	protection, err := l.security.protect()
	if err != nil || !launchProtectionSafe(l.c, protection) || !reflect.DeepEqual(protection, l.protection) {
		return errors.Join(ErrLaunchDenied, err)
	}
	if strong {
		if l.strong == nil {
			return ErrLaunchDenied
		}
		if err := l.tokenIdentity(l.strong, 0xb); err != nil {
			return err
		}
	}
	ids, err := l.scanThreads(ctx)
	if err != nil {
		return err
	}
	if len(ids) != len(l.threads) {
		return ErrLaunchDenied
	}
	for i, held := range l.threads {
		if !rootLive(ctx) || held.ref == nil || held.before.tid != ids[i] {
			return ErrLaunchDenied
		}
		state, err := held.ref.inspect()
		if err != nil || !l.threadSafe(state, ids[i]) || !reflect.DeepEqual(state, held.before) {
			return errors.Join(ErrLaunchDenied, err)
		}
	}
	after, err := l.scanThreads(ctx)
	if err != nil || !reflect.DeepEqual(ids, after) {
		return errors.Join(ErrLaunchDenied, err)
	}
	if err := l.pre.Recheck(ctx); err != nil {
		return err
	}
	binding, err = l.pre.Binding(ctx)
	if err != nil || binding != l.binding {
		return errors.Join(ErrLaunchDenied, err)
	}
	return l.subject(ctx)
}
func (l *LaunchToken) enter(ctx context.Context) error {
	if l == nil {
		return ErrLaunchDenied
	}
	if l.poisoned.Load() {
		return ErrLaunchFatal
	}
	if !rootLive(ctx) || !l.accepted.Load() || l.revoked.Load() || l.closed.Load() {
		return ErrLaunchDenied
	}
	if !rootAdmit(ctx, &l.gate) {
		return ErrLaunchDenied
	}
	if l.poisoned.Load() {
		l.gate.Unlock()
		return ErrLaunchFatal
	}
	if l.revoked.Load() || l.closed.Load() {
		l.gate.Unlock()
		return ErrLaunchDenied
	}
	return nil
}
func (l *LaunchToken) Recheck(ctx context.Context) (result error) {
	if l == nil {
		return ErrLaunchDenied
	}
	defer func() {
		if result != nil {
			l.fail(result)
		}
	}()
	if result = l.enter(ctx); result != nil {
		return result
	}
	defer l.gate.Unlock()
	result = l.verify(ctx, true)
	if result == nil && (l.revoked.Load() || l.closed.Load()) {
		result = ErrLaunchDenied
	}
	return result
}
func (l *LaunchToken) Borrow(ctx context.Context) (value uintptr, result error) {
	if l == nil {
		return 0, ErrLaunchDenied
	}
	defer func() {
		if result != nil {
			l.fail(result)
		}
	}()
	if result = l.enter(ctx); result != nil {
		return 0, result
	}
	defer l.gate.Unlock()
	if result = l.verify(ctx, true); result != nil {
		return 0, result
	}
	if l.revoked.Load() || l.closed.Load() {
		return 0, ErrLaunchDenied
	}
	return l.strong.value(), nil
}
func (l *LaunchToken) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.closed.Store(true)
	l.revoked.Store(true)
	l.accepted.Store(false)
	if l.api != nil && l.thread != 0 && l.api.threadID() != l.thread {
		l.poisoned.Store(true)
	}
	if !rootFinite(ctx) {
		return l.finish(errors.Join(ErrLaunchConfig, ErrLaunchUnconfirmed))
	}
	deadline, _ := ctx.Deadline()
	if end := time.Now().Add(l.c.CleanupTimeout); end.Before(deadline) {
		deadline = end
	}
	cleanup, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if !rootAdmit(cleanup, &l.gate) {
		return l.finish(ErrLaunchUnconfirmed)
	}
	defer l.gate.Unlock()
	if !l.poisoned.Load() && l.identity.tokenID != 0 && rootLive(cleanup) {
		if err := l.subject(cleanup); errors.Is(err, ErrLaunchFatal) {
			l.poisoned.Store(true)
		}
	}
	var result error
	closeRef := func(ref interface{ close() error }) error {
		if !rootLive(cleanup) {
			return ErrLaunchUnconfirmed
		}
		return ref.close()
	}
	if l.strong != nil {
		if err := closeRef(l.strong); err != nil {
			result = errors.Join(result, ErrLaunchUnconfirmed, err)
		} else {
			l.strong = nil
		}
	}
	if l.snapshot != nil {
		if err := closeRef(l.snapshot); err != nil {
			result = errors.Join(result, ErrLaunchUnconfirmed, err)
		} else {
			l.snapshot = nil
		}
	}
	for i := len(l.threads) - 1; i >= 0; i-- {
		if l.threads[i].ref != nil {
			if err := closeRef(l.threads[i].ref); err != nil {
				result = errors.Join(result, ErrLaunchUnconfirmed, err)
			} else {
				l.threads[i].ref = nil
			}
		}
	}
	if l.security != nil {
		if err := closeRef(l.security); err != nil {
			result = errors.Join(result, ErrLaunchUnconfirmed, err)
		} else {
			l.security = nil
		}
	}
	if l.pre != nil {
		if !rootLive(cleanup) {
			result = errors.Join(result, ErrLaunchUnconfirmed)
		} else {
			err := l.pre.Close(cleanup)
			if err == nil || err == serviceready.ErrFatal {
				l.pre = nil
			}
			if err != nil {
				if errors.Is(err, ErrLaunchFatal) {
					l.poisoned.Store(true)
				}
				if err == serviceready.ErrFatal {
					result = errors.Join(result, err)
				} else {
					result = errors.Join(result, ErrLaunchUnconfirmed, err)
				}
			}
		}
	}
	return l.finish(result)
}
func (l *LaunchToken) finish(err error) error {
	if errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal) {
		l.poisoned.Store(true)
	}
	if l.poisoned.Load() {
		return errors.Join(ErrLaunchFatal, err)
	}
	return err
}
func launchCloneConfig(c serviceready.Config) serviceready.Config {
	c.ServiceSecurity.ACL = append([]protectedkey.ACE(nil), c.ServiceSecurity.ACL...)
	c.ProcessSecurity.ACL = append([]protectedkey.ACE(nil), c.ProcessSecurity.ACL...)
	c.Profile.SupervisorImage.Nodes = launchCloneNodes(c.Profile.SupervisorImage.Nodes)
	c.Profile.ChildImage.Nodes = launchCloneNodes(c.Profile.ChildImage.Nodes)
	catalog := make(map[string]protectedkey.KeyProfile, len(c.Profile.Keys.Catalog))
	for ref, key := range c.Profile.Keys.Catalog {
		key.Nodes = launchCloneNodes(key.Nodes)
		catalog[ref] = key
	}
	c.Profile.Keys.Catalog = catalog
	return c
}
func launchCloneNodes(nodes []protectedkey.NodePolicy) []protectedkey.NodePolicy {
	out := append([]protectedkey.NodePolicy(nil), nodes...)
	for i := range out {
		out[i].ACL = append([]protectedkey.ACE(nil), out[i].ACL...)
	}
	return out
}
