package operatorauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

type reportOwnedIO struct {
	mu                        sync.Mutex
	op                        reportOperation
	done, released, cancelled bool
	n                         uint32
	err                       error
}

func (o *reportOwnedIO) step(cancel bool) (bool, uint32, error) {
	// 他callerの停止中pollに、cleanup callerを無期限に並ばせない。
	if !o.mu.TryLock() {
		return false, 0, ErrUnconfirmed
	}
	defer o.mu.Unlock()
	if cancel && !o.done && !o.cancelled {
		o.cancelled = true
		_ = o.op.cancel()
	}
	if !o.done {
		o.done, o.n, o.err = o.op.poll()
	}
	if !o.done {
		return false, 0, nil
	}
	if !o.released {
		if err := o.op.release(); err != nil {
			return false, 0, err
		}
		o.released = true
	}
	if o.cancelled {
		return true, o.n, errors.Join(context.Canceled, o.err)
	}
	return true, o.n, o.err
}
func (o *reportOwnedIO) wait(ctx context.Context, budget time.Duration, cancel bool) (uint32, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, errReportInvalid
	}
	end := deadline
	if cancel {
		budgetEnd := time.Now().Add(budget)
		if budgetEnd.Before(end) {
			end = budgetEnd
		}
	}
	for {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			cancel = true
		}
		done, n, err := o.step(cancel)
		if done {
			if cancel {
				return 0, errors.Join(ctx.Err(), context.Canceled, err)
			}
			return n, err
		}
		// 取消済みcontextではCancelIoExと一度の非blocking pollだけで返す。
		// 元のdeadlineを内部cleanup予算で延ばさず、reportOwnerを次のCloseへ残す。
		if ctx.Err() != nil || !time.Now().Before(end) {
			return 0, errors.Join(ErrUnconfirmed, ctx.Err(), err)
		}
		delay := time.Millisecond
		if remaining := time.Until(end); remaining < delay {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
