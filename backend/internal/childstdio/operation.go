package childstdio

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

type ownedOperation struct {
	mu                        sync.Mutex
	raw                       operation
	done, released, cancelled bool
	n                         uint32
	err                       error
}

func (o *ownedOperation) step(cancel bool) (bool, uint32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stepLocked(cancel)
}
func (o *ownedOperation) tryStep(cancel bool) (bool, uint32, error) {
	if !o.mu.TryLock() {
		return false, 0, ErrUnconfirmed
	}
	defer o.mu.Unlock()
	return o.stepLocked(cancel)
}
func (o *ownedOperation) stepLocked(cancel bool) (bool, uint32, error) {
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
	if !o.released {
		if err := o.raw.release(); err != nil {
			return false, 0, err
		}
		o.released = true
	}
	if o.cancelled {
		return true, o.n, errors.Join(io.ErrClosedPipe, o.err)
	}
	return true, o.n, o.err
}
func (o *ownedOperation) wait(ctx context.Context) (uint32, error) {
	for {
		cancel := ctx.Err() != nil
		deadline, _ := ctx.Deadline()
		if !time.Now().Before(deadline) {
			cancel = true
		}
		done, n, err := o.step(cancel)
		if done {
			if cancel {
				return 0, errors.Join(ctx.Err(), context.Canceled, err)
			}
			return n, err
		}
		if cancel {
			return 0, errors.Join(ErrUnconfirmed, ctx.Err(), err)
		}
		if err != nil {
			return 0, errors.Join(ErrUnconfirmed, err)
		}
		pause(ctx)
	}
}
