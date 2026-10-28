package serviceprovision

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type rootOperation interface {
	poll() (bool, uint32, error)
	cancel() error
	release() error
}
type rootOwnedOperation struct {
	mu        sync.Mutex
	raw       rootOperation
	done      bool
	n         uint32
	err       error
	cancelled bool
	released  atomic.Bool
}

func (o *rootOwnedOperation) tryStep(cancel bool) (bool, uint32, error) {
	if !o.mu.TryLock() {
		return false, 0, ErrRootUnconfirmed
	}
	defer o.mu.Unlock()
	if cancel && !o.done && !o.cancelled {
		o.cancelled = true
		_ = o.raw.cancel()
	}
	if !o.done {
		o.done, o.n, o.err = o.raw.poll()
	}
	if !o.done {
		return false, 0, o.err
	}
	if !o.released.Load() {
		if err := o.raw.release(); err != nil {
			return false, 0, err
		}
		o.released.Store(true)
	}
	return true, o.n, o.err
}
func (l *Loaded) readFile(ctx context.Context, h *rootHeld) ([]byte, error) {
	if !rootLive(ctx) {
		return nil, ErrRootDenied
	}
	buffer := make([]byte, int(h.before.size)+1)
	raw, err := h.file.read(buffer)
	if raw != nil {
		h.op = &rootOwnedOperation{raw: raw}
	}
	if err != nil || raw == nil {
		return nil, errors.Join(ErrRootDenied, err)
	}
	for {
		cancelled := ctx.Err() != nil
		deadline, _ := ctx.Deadline()
		if !time.Now().Before(deadline) {
			cancelled = true
		}
		done, n, err := h.op.tryStep(cancelled)
		if done {
			if cancelled || err != nil || n != uint32(h.before.size) {
				return nil, errors.Join(ErrRootDenied, ctx.Err(), err)
			}
			return append([]byte(nil), buffer[:n]...), nil
		}
		if cancelled || err != nil {
			return nil, errors.Join(ErrRootUnconfirmed, ctx.Err(), err)
		}
		rootPause(ctx)
	}
}
