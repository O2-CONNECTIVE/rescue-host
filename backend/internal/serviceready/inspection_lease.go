package serviceready

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

const endpointInspectionAccess uint32 = 0x120080

type inspectionBinding struct {
	profile  bootstrapauth.Profile
	pid      uint32
	creation uint64
}
type inspectionSubject struct {
	user                                  string
	kind, serviceAttributes               uint32
	tokenID, authenticationID, modifiedID uint64
	restricted, appContainer              bool
}
type inspectionState struct {
	kind, access, flags, serverPID, currentPID uint32
	creation                                   uint64
	security                                   SecurityPolicy
	subject                                    inspectionSubject
}
type inspectionHandle interface {
	endpointHandle
	inspectInspection(context.Context, inspectionBinding) (inspectionState, error)
	inspectionSubject(context.Context, inspectionBinding) (inspectionSubject, error)
	borrowInspection() (uintptr, error)
}

// native holderと終端を共有し、fatal時はwork/pinを保持する。
// 最終主体照合では要求取消と回収確認を分離する。
type inspectionNativeCompletion struct{ fatal bool }

func (c *inspectionNativeCompletion) complete(err error, terminal func() error, release func()) error {
	if c.fatal || errors.Is(err, ErrFatal) {
		c.fatal = true
		return ErrFatal
	}
	if terminal() != nil {
		c.fatal = true
		return ErrFatal
	}
	release()
	return err
}

// InspectionLeaseは読取可能handleや認証/承認証拠を発行しない。
// Borrowは一時借用であり、callerは全queryの実終了後にだけCloseする。
// Endpointは同じ複製のclose確認まで部分leaseを保持する。
// 各callはcallerの固定OS threadで完結し、発行workerの専用scopeを再入しない。
type InspectionLease struct {
	self                           *InspectionLease
	mu                             sync.Mutex
	endpoint                       *Endpoint
	binding                        inspectionBinding
	ref                            endpointHandle
	subject                        inspectionSubject
	bound, closed, failed, unknown bool
}

func (e *Endpoint) NewInspectionLease(ctx context.Context) (lease *InspectionLease, err error) {
	if !e.canonical() {
		return nil, ErrDenied
	}
	if err = e.enter(ctx); err != nil {
		return nil, e.fail(err)
	}
	defer e.query.gate.Unlock()
	defer func() {
		if recover() != nil {
			err = ErrFatal
			if lease != nil {
				lease.unknown = true
			}
		}
		if err != nil {
			e.fail(err)
			if lease != nil {
				lease.failed = true
				if errors.Is(err, ErrFatal) {
					lease.unknown = true
				}
			}
		}
	}()
	if !e.connected || e.duplex == nil || e.inspection != nil {
		return nil, ErrDenied
	}
	// 複製dispatch前に予約・保持し、部分return/panicでも同じ参照を残す。
	lease = &InspectionLease{endpoint: e, binding: inspectionBinding{profile: cloneConfig(e.query.c).Profile, pid: e.query.pid, creation: e.query.creation}}
	lease.self = lease
	e.inspection = lease
	e.inspectionActive.Add(1)
	if err = e.inspectDuplex(); err != nil {
		return lease, err
	}
	if err = e.check(ctx); err != nil {
		return lease, err
	}
	lease.unknown = true
	lease.ref, err = e.duplex.duplicate(endpointInspectionAccess)
	lease.unknown = false
	if err != nil || lease.ref == nil {
		return lease, errors.Join(ErrDenied, err)
	}
	if err = lease.ref.validate(endpointInspectionAccess); err != nil {
		return lease, err
	}
	if err = e.check(ctx); err != nil {
		return lease, err
	}
	if err = lease.inspect(ctx); err != nil {
		return lease, err
	}
	if err = e.inspectDuplex(); err != nil {
		return lease, err
	}
	return lease, e.check(ctx)
}
func (l *InspectionLease) valid() bool {
	return l != nil && l.self == l && l.endpoint != nil && l.endpoint.self == l.endpoint
}

// MatchesProfileは発行元の不変profileとの純粋比較。認証や承認証拠を追加しない。
func (l *InspectionLease) MatchesProfile(profile bootstrapauth.Profile) bool {
	if !l.valid() || !l.mu.TryLock() {
		return false
	}
	defer l.mu.Unlock()
	return !l.closed && !l.unknown && !l.failed && reflect.DeepEqual(l.binding.profile, profile)
}
func (l *InspectionLease) admitted(ctx context.Context) error {
	q := l.endpoint.query
	if q.poisoned.Load() {
		return ErrFatal
	}
	if !serviceWitnessLive(ctx) || l.endpoint.failed.Load() || l.endpoint.closing.Load() || l.endpoint.closed.Load() != nil || q.revoked.Load() || q.stopped.Load() {
		return ErrDenied
	}
	return nil
}
func validInspectionSubject(s inspectionSubject, b inspectionBinding) bool {
	return s.user == b.profile.Keys.ProcessUserSID && s.kind == 1 && s.serviceAttributes&4 != 0 && s.serviceAttributes&0x10 == 0 && !s.restricted && !s.appContainer && s.tokenID != 0 && s.authenticationID != 0 && s.modifiedID != 0
}
func (l *InspectionLease) inspect(ctx context.Context) error {
	if err := l.admitted(ctx); err != nil {
		return err
	}
	h, ok := l.ref.(inspectionHandle)
	if !ok {
		return ErrUnavailable
	}
	s, err := h.inspectInspection(ctx, l.binding)
	if err != nil {
		return err
	}
	if !validInspectionSubject(s.subject, l.binding) || l.bound && l.subject != s.subject {
		return ErrFatal
	}
	if s.kind != 3 || s.access != endpointInspectionAccess || s.flags != 0 || s.serverPID != l.binding.pid || s.currentPID != l.binding.pid || s.creation == 0 || s.creation != l.binding.creation || !sameSecurity(s.security, listenerSecurity(l.binding.profile)) {
		return ErrDenied
	}
	l.subject = s.subject
	l.bound = true
	return l.admitted(ctx)
}
func (l *InspectionLease) Recheck(ctx context.Context) error { _, err := l.Borrow(ctx); return err }
func (l *InspectionLease) Borrow(ctx context.Context) (raw uintptr, err error) {
	if !l.valid() {
		return 0, ErrDenied
	}
	if !l.mu.TryLock() {
		return 0, ErrDenied
	}
	defer l.mu.Unlock()
	defer func() {
		if recover() != nil {
			l.unknown = true
			err = ErrFatal
		}
		if err != nil {
			raw = 0
			l.failed = true
			if errors.Is(err, ErrFatal) {
				l.unknown = true
			}
			l.endpoint.fail(err)
		}
	}()
	if l.unknown {
		return 0, ErrFatal
	}
	if l.closed || l.failed {
		return 0, ErrDenied
	}
	if err = l.inspect(ctx); err != nil {
		return 0, err
	}
	raw, err = l.ref.(inspectionHandle).borrowInspection()
	if raw == 0 || raw >= ^uintptr(15) {
		return 0, errors.Join(ErrDenied, err)
	}
	if err != nil {
		return 0, err
	}
	return raw, l.admitted(ctx)
}
func (l *InspectionLease) Close(ctx context.Context) (err error) {
	if !l.valid() {
		return ErrDenied
	}
	if !l.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer l.mu.Unlock()
	defer func() {
		if recover() != nil {
			l.unknown = true
			err = ErrFatal
		}
		if err != nil {
			l.failed = true
			if errors.Is(err, ErrFatal) {
				l.unknown = true
			}
			l.endpoint.fail(err)
		}
	}()
	if l.closed {
		return nil
	}
	if l.unknown {
		return errors.Join(ErrFatal, ErrUnconfirmed)
	}
	if !serviceWitnessLive(ctx) {
		return ErrUnconfirmed
	}
	if l.ref != nil {
		h, ok := l.ref.(inspectionHandle)
		if !ok {
			return ErrUnconfirmed
		}
		s, queryErr := h.inspectionSubject(ctx, l.binding)
		if queryErr != nil {
			return queryErr
		}
		if !validInspectionSubject(s, l.binding) || l.bound && s != l.subject {
			return ErrFatal
		}
		if !serviceWitnessLive(ctx) {
			return ErrUnconfirmed
		}
		closeErr := l.ref.close()
		if errors.Is(closeErr, ErrFatal) {
			return closeErr
		}
		after, queryErr := h.inspectionSubject(ctx, l.binding)
		if queryErr != nil {
			return queryErr
		}
		if !validInspectionSubject(after, l.binding) || after != s {
			return ErrFatal
		}
		if closeErr != nil {
			return errors.Join(ErrUnconfirmed, closeErr)
		}
		if !serviceWitnessLive(ctx) {
			return ErrUnconfirmed
		}
		l.ref = nil
	}
	l.closed = true
	l.endpoint.inspectionActive.Add(-1)
	return nil
}
