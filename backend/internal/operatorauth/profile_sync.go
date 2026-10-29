package operatorauth

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

type profileSyncKind uint8

const (
	profileSyncRead profileSyncKind = iota + 1
	profileSyncWrite
)

var errProfileSyncEOF = errors.New("actual synchronous profile pipe EOF")

// This private seam performs one synchronous call on one dedicated OS thread.
// The endpoint owner must retain the handle and surrender buffer access until
// release succeeds. It must not close the endpoint while this owner is retained.
// Consecutive reads require individually joined owners; this is not a framing or
// EOF proof. A writer may start only after its caller has resumed the child.
type profileSyncAPI interface {
	currentThread() (uintptr, error)
	transfer(profileSyncKind, uintptr, []byte) (uint32, error)
	cancel(uintptr) error
	closeThread(uintptr) error
}

type profileSyncOwner struct {
	mu, stepMu        sync.Mutex
	api               profileSyncAPI
	ctx               context.Context
	handle            uintptr
	buffer            []byte
	thread            uintptr
	stopped, returned bool
	uncertain         bool
	closeConfirmed    bool
	n                 uint32
	err               error
	joined            chan struct{}
	closeRequests     chan chan error
	cancelResult      chan error
	closeResult       chan error
	unknownHold       chan struct{}
}

func startProfileSync(ctx context.Context, api profileSyncAPI, kind profileSyncKind, handle uintptr, buffer []byte) (*profileSyncOwner, error) {
	if !reportContextValid(ctx) || api == nil || (kind != profileSyncRead && kind != profileSyncWrite) || !reportHandle(handle) || len(buffer) == 0 || len(buffer) > 16385 {
		return nil, ErrDenied
	}
	o := &profileSyncOwner{api: api, ctx: ctx, handle: handle, buffer: buffer, joined: make(chan struct{}), closeRequests: make(chan chan error, 1), unknownHold: make(chan struct{})}
	go o.run(ctx, kind)
	return o, nil
}

func startNativeProfileSync(ctx context.Context, kind profileSyncKind, handle uintptr, buffer []byte) (*profileSyncOwner, error) {
	if !reportContextValid(ctx) || (kind != profileSyncRead && kind != profileSyncWrite) || !reportHandle(handle) || len(buffer) == 0 || len(buffer) > 16385 {
		return nil, ErrDenied
	}
	api, err := nativeProfileSyncAPI()
	if err != nil {
		return nil, err
	}
	return startProfileSync(ctx, api, kind, handle, buffer)
}

func (o *profileSyncOwner) run(ctx context.Context, kind profileSyncKind) {
	runtime.LockOSThread()
	defer func() {
		if recover() != nil {
			o.markUncertain()
			// No return/close proof: retain the locked worker permanently. A
			// reference acquired inside a panicking currentThread but never
			// returned cannot be reconstructed by this seam.
			<-o.unknownHold
		}
	}()
	thread, err := o.api.currentThread()
	if !reportHandle(thread) {
		thread = 0
		err = errors.Join(ErrDenied, err)
	}
	o.mu.Lock()
	o.thread = thread
	stop := o.stopped || !reportContextValid(ctx)
	if stop {
		o.stopped = true
	}
	o.mu.Unlock()
	var n uint32
	if err == nil && !stop {
		// Cancellation between this check and the actual call is covered by
		// repeated attempts, including after ERROR_NOT_FOUND.
		n, err = o.api.transfer(kind, o.handle, o.buffer)
	}
	o.mu.Lock()
	o.n, o.err, o.returned = n, err, true
	o.mu.Unlock()
	for response := range o.closeRequests {
		var closeErr error
		if reportHandle(thread) {
			closeErr = o.api.closeThread(thread)
		}
		response <- closeErr
		if closeErr == nil {
			runtime.UnlockOSThread()
			close(o.joined)
			return
		}
	}
}

func (o *profileSyncOwner) markUncertain() {
	o.mu.Lock()
	o.uncertain = true
	o.mu.Unlock()
}

func (o *profileSyncOwner) recordStop(stop bool) {
	o.mu.Lock()
	if stop || !reportContextValid(o.ctx) {
		o.stopped = true
	}
	o.mu.Unlock()
}

// step never waits for an API call. A cancellation call that has not returned
// remains attached to this owner and blocks the worker's release handshake.
func (o *profileSyncOwner) step(stop bool) (bool, uint32, error) {
	o.recordStop(stop)
	if !o.stepMu.TryLock() {
		return false, 0, ErrUnconfirmed
	}
	defer o.stepMu.Unlock()
	select {
	case <-o.joined:
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.stopped {
			return true, 0, errors.Join(context.Canceled, o.err)
		}
		return true, o.n, o.err
	default:
	}
	if o.cancelResult != nil {
		select {
		case <-o.cancelResult:
			o.cancelResult = nil
		default:
		}
	}
	if o.closeResult != nil {
		select {
		case err := <-o.closeResult:
			o.closeResult = nil
			if err != nil {
				return false, 0, err
			}
			o.closeConfirmed = true
		default:
		}
	}
	o.mu.Lock()
	if o.uncertain {
		o.mu.Unlock()
		return false, 0, ErrUnconfirmed
	}
	returned, stopped, thread := o.returned, o.stopped, o.thread
	o.mu.Unlock()
	if returned && o.cancelResult == nil && o.closeResult == nil && !o.closeConfirmed {
		o.closeResult = make(chan error, 1)
		o.closeRequests <- o.closeResult
	} else if stopped && !returned && reportHandle(thread) && o.cancelResult == nil {
		result := make(chan error, 1)
		o.cancelResult = result
		go func() {
			var err error
			defer func() {
				if recover() != nil {
					o.markUncertain()
					err = ErrUnconfirmed
				}
				result <- err
			}()
			err = o.api.cancel(thread)
		}()
	}
	return false, 0, nil
}

func (o *profileSyncOwner) wait(ctx context.Context, budget time.Duration, stop bool) (uint32, error) {
	if ctx == nil || budget <= 0 {
		return 0, ErrDenied
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, ErrDenied
	}
	if end := time.Now().Add(budget); end.Before(deadline) {
		deadline = end
	}
	for {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			stop = true
		}
		done, n, err := o.step(stop)
		if done {
			if ctx.Err() != nil || !time.Now().Before(deadline) {
				o.recordStop(true)
				return 0, errors.Join(context.Canceled, ctx.Err(), err)
			}
			return n, err
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			o.recordStop(true)
			return 0, errors.Join(ErrUnconfirmed, ctx.Err(), err)
		}
		timer := time.NewTimer(min(time.Millisecond, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Failure leaves the endpoint reference, buffer, worker and thread with o.
func (o *profileSyncOwner) release() error {
	if !o.stepMu.TryLock() {
		return ErrUnconfirmed
	}
	defer o.stepMu.Unlock()
	select {
	case <-o.joined:
		o.handle, o.buffer, o.api = 0, nil, nil
		return nil
	default:
		return ErrUnconfirmed
	}
}
