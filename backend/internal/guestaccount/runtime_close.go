package guestaccount

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

// Revoke is only the nonblocking local admission latch. The Service separately
// commits canonical issuer revocation before waiting for native work to return.
func (r *RuntimeGuest) Revoke() error {
	if r == nil || r.self != r {
		return ErrDenied
	}
	r.stop.Store(true)
	if p := r.prepared.Load(); p != nil {
		return p.Revoke()
	}
	return nil
}
func (r *RuntimeGuest) Close(ctx context.Context) (result error) {
	if r == nil || r.self != r {
		return ErrCleanup
	}
	_ = r.Revoke()
	if !finite(ctx) || !r.mu.TryLock() {
		return ErrBusy
	}
	defer r.finish(&result)
	if r.closed {
		return nil
	}
	if r.fatal {
		return bootstrapauth.ErrFatal
	}
	if s, ok := r.stage.(*stagingOwner); ok && s != nil && (s.blocked || s.fatal) {
		return ErrUnknown
	}
	if r.stage != nil && !r.ops.guard() {
		if s, ok := r.stage.(*stagingOwner); ok && s != nil && s.blocked {
			return ErrUnknown
		}
		r.fatal = true
		return bootstrapauth.ErrFatal
	}
	if r.credential != nil {
		if err := r.credential.Close(ctx); err != nil {
			return err
		}
		clearRuntimeSecret(r.secret)
	}
	if r.created.sid == "" {
		if err := r.closeRuntime(ctx); err != nil {
			return err
		}
	}
	if r.stage == nil {
		if err := r.authority.revoke(ctx, r.plan.binding); err != nil {
			return err
		}
	} else {
		if err := r.stage.Close(ctx); err != nil {
			return err
		}
	}
	if r.lease != nil {
		if err := r.lease.Close(ctx); err != nil {
			return err
		}
	}
	r.closed = true
	return nil
}
