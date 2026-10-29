package guestaccount

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type Lifecycle struct {
	self                                       *Lifecycle
	mu                                         sync.Mutex
	stop                                       atomic.Bool
	plan                                       Plan
	sources                                    sources
	record                                     record
	state                                      State
	unknown, deletionPending, deletionObserved bool
	recovering                                 bool
	revocationConfirmed                        bool
}

// NewNative は全OSで閉じる。アカウントも資格情報も作成しない。
func NewNative() (*Lifecycle, error) { return nil, ErrUnconfigured }

func newLifecycle(p Plan, s sources) (*Lifecycle, error) {
	if !configured(p, s) || s.session == nil {
		return nil, ErrUnconfigured
	}
	l := &Lifecycle{plan: p, sources: s, record: record{binding: p.binding}, state: Planned}
	l.self = l
	return l, nil
}
func configured(p Plan, s sources) bool {
	return p.valid && s.journal != nil && s.accounts != nil && s.journalSeal != nil && s.accountSeal != nil && s.sessionSeal != nil
}
func (l *Lifecycle) valid() bool { return l != nil && l.self == l }
func finite(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	d, ok := ctx.Deadline()
	return ok && time.Now().Before(d)
}
func (l *Lifecycle) Status() (Status, error) {
	if !l.valid() {
		return Status{State: Unconfigured}, ErrUnconfigured
	}
	if !l.mu.TryLock() {
		return Status{AdmissionClosed: l.stop.Load()}, ErrBusy
	}
	defer l.mu.Unlock()
	return Status{State: l.state, Name: l.record.binding.name, SID: l.record.binding.sid, CleanupRequired: l.state != Planned && l.state != Closed, Uncertain: l.unknown, AdmissionClosed: l.stop.Load(), RevocationConfirmed: l.revocationConfirmed}, nil
}

func (l *Lifecycle) enter(ctx context.Context) error {
	if !l.valid() {
		return ErrUnconfigured
	}
	if !finite(ctx) {
		return ErrDenied
	}
	if !l.mu.TryLock() {
		return ErrBusy
	}
	if !finite(ctx) {
		l.mu.Unlock()
		return ErrDenied
	}
	return nil
}

// 呼出しが実際にreturnするまでsourceとgateの所有を手放さない。
func (l *Lifecycle) finish(result *error) {
	if recover() != nil {
		l.unknown = true
		l.state = Unknown
		*result = ErrUnknown
	}
	if l.stop.Load() && l.state == Prepared {
		l.state = Cleaning
		if *result == nil {
			*result = ErrCleanup
		}
	}
	l.mu.Unlock()
}
