package servicehost

import (
	"context"
	"errors"
	"runtime"
	"time"
)

// runOwner encloses all owned work, including Endpoint cleanup. Its caller is a
// dedicated worker and performs no further owned/native work after this returns.
// #127 acquires the Batch ServiceScope inside Start on this same pinned worker.
func (r *serviceAuthRequest) runOwner(action func() error) (result error) {
	if r == nil || r.adapter == nil || action == nil {
		return ErrProvisioningRejected
	}
	s := r.adapter.service
	s.mu.Lock()
	if r.ownerRunning || r.ownerEndedLocked() || r.finished || s.auth != r.adapter || r.adapter.active != r {
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	r.ownerRunning = true
	s.mu.Unlock()
	runtime.LockOSThread()
	defer func() {
		if recover() != nil {
			result = ErrRuntimeUnconfirmed
		}
		s.mu.Lock()
		r.finalizing = true
		s.mu.Unlock()
		if result == nil {
			cleanup, cancel := context.WithTimeout(context.Background(), s.cfg.Budgets.Cleanup)
			result = r.closeNormal(cleanup)
			cancel()
		}
		if result != nil {
			s.stopFor(result) // admission/revocation commits before worker cancellation.
			if !authFatal(result) {
				cleanup, cancel := context.WithTimeout(context.Background(), s.cfg.Budgets.Cleanup)
				closeErr := r.close(cleanup)
				cancel()
				if closeErr == nil {
					r.finish()
				} else {
					result = errors.Join(result, closeErr)
					if !authFatal(closeErr) {
						result = errors.Join(result, r.awaitCleanup())
					}
				}
			}
		} else {
			r.finish()
		}
		s.mu.Lock()
		if authFatal(result) {
			r.fatal = authFailure(result, ErrRuntimeUnconfirmed)
		}
		fatal := r.fatal
		s.mu.Unlock()
		if fatal != nil {
			s.stopFor(fatal)
		}
		r.cancel()
		if !authFatal(result) {
			runtime.UnlockOSThread()
		}
		// Last owned-worker action: no outer Endpoint cleanup remains after this ack.
		close(r.ownerDone)
	}()
	s.mu.Lock()
	live := r.acquisitionLiveLocked(r.worker)
	s.mu.Unlock()
	if !live {
		return ErrProvisioningRejected
	}
	return action()
}
func (r *serviceAuthRequest) closeNormal(ctx context.Context) error {
	if !authBudget(ctx) || !r.closeGate.TryLock() {
		return ErrRuntimeUnconfirmed
	}
	defer r.closeGate.Unlock()
	s := r.adapter.service
	s.mu.Lock()
	valid := r.batch.collector == nil || r.batchConsumed
	s.mu.Unlock()
	if !valid {
		return ErrRuntimeUnconfirmed
	}
	// Collect/Consume already acknowledged the exact helper lifecycle. Batch.Close
	// would revoke the canonical Owner, so only Endpoint is closed on this path.
	if err := r.closeEndpoint(ctx); err != nil {
		return err
	}
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	r.cleaned = true
	s.mu.Unlock()
	return nil
}

func (r *serviceAuthRequest) closeOwned(ctx context.Context) error {
	s := r.adapter.service
	s.mu.Lock()
	cleaned, batchClosed, batch, endpoint, fatal := r.cleaned, r.batchClosed, r.batch, r.endpoint, r.fatal
	s.mu.Unlock()
	if fatal != nil {
		return fatal
	}
	if cleaned {
		return nil
	}
	var batchErr error
	if batch.collector != nil && !batchClosed {
		batchCtx := ctx
		end := func() {}
		if endpoint != nil {
			deadline, _ := ctx.Deadline()
			batchCtx, end = context.WithTimeout(ctx, time.Until(deadline)/2)
		}
		batchErr = authCleanupCall(func() error { return batch.close(batchCtx) })
		if !authBudget(batchCtx) {
			batchErr = authFailure(batchErr, ErrRuntimeUnconfirmed)
		}
		end()
		if batchErr == nil {
			s.mu.Lock()
			r.batchClosed = true
			s.mu.Unlock()
		}
	}
	if authFatal(batchErr) {
		return authFailure(batchErr, ErrRuntimeUnconfirmed)
	}
	// Endpoint owns independent references; its ack excludes moved Batch readers.
	// Reserve part of the original deadline so an unknown Batch cannot starve it.
	endpointErr := r.closeEndpoint(ctx)
	if batchErr != nil || endpointErr != nil || !authBudget(ctx) {
		return authFailure(errors.Join(batchErr, endpointErr), ErrRuntimeUnconfirmed)
	}
	s.mu.Lock()
	r.cleaned = true
	s.mu.Unlock()
	return nil
}

// A panic has unknown cleanup outcome. Preserve that owner and continue the
// other independent owner's finite attempt; it never mints a cleanup receipt.
func authCleanupCall(action func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return action()
}
