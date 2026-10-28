package operatorauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

type reportOwner struct {
	mu                 sync.Mutex
	cleanup            sync.Mutex
	api                reportKernel
	budget             time.Duration
	read, write, child uintptr
	closed             bool
	busy               int
	pending            []*reportOwnedIO
}

func (o *reportOwner) run(ctx context.Context, kind reportKind, h uintptr, b []byte) (uint32, error) {
	// 呼出側がbusyを予約している間はCloseがhandleを解放できない。
	defer func() { o.mu.Lock(); o.busy--; o.mu.Unlock() }()
	if !reportContextValid(ctx) {
		return 0, errReportInvalid
	}
	raw, err := o.api.start(kind, h, b)
	if raw == nil {
		return 0, err
	}
	op := &reportOwnedIO{op: raw}
	o.mu.Lock()
	o.pending = append(o.pending, op)
	o.mu.Unlock()
	if err != nil {
		_, joinErr := op.wait(ctx, o.budget, true)
		return 0, errors.Join(err, joinErr)
	}
	return op.wait(ctx, o.budget, false)
}
func (o *reportOwner) unresolved() bool {
	for _, op := range o.pending {
		op.mu.Lock()
		done := op.released
		op.mu.Unlock()
		if !done {
			return true
		}
	}
	return false
}
func (o *reportOwner) closeHandle(ctx context.Context, h *uintptr) error {
	o.mu.Lock()
	value := *h
	o.mu.Unlock()
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	if value == 0 {
		return nil
	}
	// cleanup mutexでclose同士だけを直列化し、状態mutex外でOSへ渡す。
	if err := o.api.close(value); err != nil {
		return errors.Join(ErrUnconfirmed, err)
	}
	o.mu.Lock()
	*h = 0
	o.mu.Unlock()
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
func (o *reportOwner) close(ctx context.Context) error {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	cleanupCtx, cancel, err := boundedCleanup(ctx, o.budget)
	if err != nil {
		return err
	}
	defer cancel()
	if err := o.admitCleanup(cleanupCtx); err != nil {
		return err
	}
	defer o.cleanup.Unlock()
	o.mu.Lock()
	pending := append([]*reportOwnedIO(nil), o.pending...)
	busy := o.busy
	o.mu.Unlock()
	var result error
	for _, op := range pending {
		_, err := op.wait(cleanupCtx, o.budget, true)
		if errors.Is(err, ErrUnconfirmed) {
			result = errors.Join(result, err)
		}
	}
	o.mu.Lock()
	unsafe := busy != 0 || o.busy != 0 || o.unresolved()
	o.mu.Unlock()
	if unsafe {
		return errors.Join(result, ErrUnconfirmed)
	}
	for _, h := range []*uintptr{&o.write, &o.child, &o.read} {
		result = errors.Join(result, o.closeHandle(cleanupCtx, h))
	}
	return result
}
