package guestaccount

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// query の呼出しと未知参照を所有し、native producer の権限完了を代替しない。
type creationQueryOwner struct {
	self           *creationQueryOwner
	query          *accountQuery
	creation       bool
	mu             sync.Mutex
	stop           atomic.Bool
	used, poisoned bool
	work           *creationQueryWork
	active         atomic.Pointer[creationQueryWork]
}
type creationQueryWork struct {
	done     chan struct{}
	cancel   context.CancelFunc
	snapshot accountSnapshot
	err      error
}

func newCreationCheck(p Plan, s *seal, k accountQueryKernel) (*creationQueryOwner, error) {
	q, e := newAccountCreationQuery(p, s, k)
	if e != nil {
		return nil, e
	}
	return newCreationQueryOwner(q, true)
}
func newCreationInspector(p Plan, b binding, s *seal, k accountQueryKernel) (*creationQueryOwner, error) {
	q, e := newAccountQuery(p, b, s, k)
	if e != nil {
		return nil, e
	}
	return newCreationQueryOwner(q, false)
}
func newCreationQueryOwner(q *accountQuery, creation bool) (*creationQueryOwner, error) {
	if !q.valid() || creation && (!q.plan.valid || q.binding != q.plan.binding || q.binding.sid != "") || !creation && !q.plan.validBinding(q.binding) {
		return nil, ErrUnconfigured
	}
	o := &creationQueryOwner{query: q, creation: creation}
	o.self = o
	return o, nil
}
func (o *creationQueryOwner) valid() bool { return o != nil && o.self == o && o.query.valid() }
func (o *creationQueryOwner) check(ctx context.Context) error {
	if !o.valid() {
		return ErrUnconfigured
	}
	if !o.creation {
		return ErrDenied
	}
	_, e := o.dispatch(ctx, o.query.binding)
	return e
}
func (o *creationQueryOwner) inspect(ctx context.Context, b binding) (accountSnapshot, error) {
	if !o.valid() {
		return accountSnapshot{}, ErrUnconfigured
	}
	if o.creation {
		return accountSnapshot{}, ErrDenied
	}
	return o.dispatch(ctx, b)
}
func (o *creationQueryOwner) dispatch(ctx context.Context, b binding) (accountSnapshot, error) {
	if !finite(ctx) || b != o.query.binding {
		return accountSnapshot{}, ErrDenied
	}
	if o.stop.Load() {
		return accountSnapshot{}, ErrUnknown
	}
	if !o.mu.TryLock() {
		return accountSnapshot{}, ErrBusy
	}
	if o.stop.Load() || o.poisoned || o.creation && o.used {
		o.mu.Unlock()
		return accountSnapshot{}, ErrUnknown
	}
	if o.work != nil {
		select {
		case <-o.work.done:
		default:
			o.mu.Unlock()
			return accountSnapshot{}, ErrBusy
		}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w := &creationQueryWork{done: make(chan struct{}), cancel: cancel}
	o.work = w
	o.active.Store(w)
	o.used = true
	o.mu.Unlock()
	go o.run(workerCtx, b, w)
	select {
	case <-w.done:
		if !finite(ctx) || o.stop.Load() {
			o.stopWork()
			return accountSnapshot{}, ErrUnknown
		}
		return w.snapshot, w.err
	case <-ctx.Done():
		// caller が先に戻っても worker/query と資源は同じ owner に残す。
		o.stopWork()
		return accountSnapshot{}, ErrUnknown
	}
}
func (o *creationQueryOwner) run(ctx context.Context, b binding, w *creationQueryWork) {
	defer func() {
		if recover() != nil {
			o.mu.Lock()
			o.poisoned = true
			o.mu.Unlock()
			w.snapshot = accountSnapshot{}
			w.err = ErrUnknown
		}
		w.cancel()
		close(w.done)
	}()
	if o.stop.Load() {
		w.err = ErrUnknown
		return
	}
	if o.creation {
		w.err = o.query.creationCheck(ctx)
	} else {
		w.snapshot, w.err = o.query.inspect(ctx, b)
	}
	// q が fatal で返った thread は unlock されていない。この後は Go の
	// 所有記録だけを行って goroutine を終了し、native cleanup を再開しない。
	o.query.mu.Lock()
	blocked := o.query.fatal || len(o.query.pending) != 0
	o.query.mu.Unlock()
	var fatal accountQueryScopeFatal
	if blocked || errors.As(w.err, &fatal) {
		o.mu.Lock()
		o.poisoned = true
		o.mu.Unlock()
		w.snapshot = accountSnapshot{}
		w.err = ErrUnknown
	}
}

func (o *creationQueryOwner) stopWork() {
	o.stop.Store(true)
	if w := o.active.Load(); w != nil {
		w.cancel()
	}
}
func (o *creationQueryOwner) Close(ctx context.Context) error {
	if !o.valid() {
		return ErrUnconfigured
	}
	o.stopWork()
	if !finite(ctx) || !o.mu.TryLock() {
		return ErrUnknown
	}
	w, blocked := o.work, o.poisoned
	o.mu.Unlock()
	if blocked {
		return ErrUnknown
	}
	if w != nil {
		select {
		case <-w.done:
		case <-ctx.Done():
			return ErrUnknown
		}
	}
	if !finite(ctx) || !o.mu.TryLock() {
		return ErrUnknown
	}
	blocked = o.poisoned
	o.mu.Unlock()
	if blocked {
		return ErrUnknown
	}
	return nil
}
