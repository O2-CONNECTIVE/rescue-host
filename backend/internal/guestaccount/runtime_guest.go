package guestaccount

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// RuntimeGuest retains one actual creator receipt, credential, primary token and
// fixed workload. It never accepts a caller's raw SID, password or primary token.
// Native acquisition and Close run on the original execution owner thread.
type RuntimeGuest struct {
	self                *RuntimeGuest
	mu                  sync.Mutex
	stop                atomic.Bool
	plan                Plan
	probe               childjob.Spec
	stage               runtimeStage
	lifecycle           *Lifecycle
	prepareContext      context.Context
	lease               *serviceready.InspectionLease
	authority           runtimeAuthority
	ops                 runtimeOps
	created             binding
	sessionSeal         *seal
	rights              runtimeRights
	credential          runtimeCredential
	token               runtimeToken
	target              runtimeTarget
	prepared            atomic.Pointer[childjob.Prepared]
	secret              []uint16
	used, closed, fatal bool
}

func newRuntimeGuest(p Plan, a runtimeAuthority, ops runtimeOps) (*RuntimeGuest, error) {
	if !p.valid || !p.policy.batch || a == nil || ops.guard == nil || ops.random == nil || ops.rights == nil || ops.credential == nil || ops.token == nil || ops.target == nil || ops.check == nil {
		return nil, ErrUnconfigured
	}
	r := &RuntimeGuest{plan: p, authority: a, ops: ops}
	r.self = r
	return r, nil
}
func (r *RuntimeGuest) live(ctx context.Context) bool {
	return finite(ctx) && !r.stop.Load() && r.ops.guard()
}
func (r *RuntimeGuest) finish(result *error) {
	if recover() != nil {
		r.fatal = true
		*result = bootstrapauth.ErrFatal
	}
	if errors.Is(*result, bootstrapauth.ErrFatal) {
		r.fatal = true
	}
	if r.fatal {
		*result = bootstrapauth.ErrFatal
	}
	r.mu.Unlock()
}

// Prepare runs only this owner's frozen harmless probe. It owns every partial
// return before checking cancellation/error and accepts no command or handle.
func (r *RuntimeGuest) Prepare(ctx context.Context) (p *childjob.Prepared, result error) {
	if r == nil || r.self != r || !finite(ctx) || !r.mu.TryLock() {
		return nil, ErrDenied
	}
	defer r.finish(&result)
	spec := r.probe
	if r.used || r.closed || r.fatal || r.stage == nil || r.lifecycle == nil || r.lifecycle.sources.session != r || !r.live(ctx) || spec.PrimaryToken != 0 || spec.Handles != (childjob.Handles{}) || spec.Bootstrap != nil {
		return nil, ErrDenied
	}
	r.used = true
	r.prepareContext = ctx
	r.credential = r.ops.credential()
	if r.credential == nil {
		return nil, ErrUnconfigured
	}
	if err := r.credential.prepare(ctx, r.ops.guard); err != nil || !r.live(ctx) {
		return nil, ErrUnconfigured
	}
	r.rights = r.ops.rights()
	if r.rights == nil {
		return nil, ErrUnconfigured
	}
	if err := r.stage.Prepare(ctx); err != nil {
		return nil, err
	}
	if r.lifecycle.state != Prepared || r.created != r.lifecycle.record.binding || !r.plan.validBinding(r.created) || !r.live(ctx) {
		return nil, ErrDenied
	}
	if err := r.ops.check(ctx, r.created); err != nil || !r.live(ctx) {
		return nil, ErrDenied
	}
	var err error
	r.secret, err = runtimeSecret(r.ops.random)
	if err != nil || !r.live(ctx) {
		return nil, ErrDenied
	}
	if err = r.credential.provision(ctx, r.created, r.secret, r.ops.guard); err != nil || !r.live(ctx) {
		return nil, ErrDenied
	}
	r.token = r.ops.token()
	if r.token == nil {
		return nil, ErrUnconfigured
	}
	if err = r.token.acquire(ctx, r.created, r.secret, r.ops.guard); err != nil || !r.live(ctx) {
		return nil, ErrDenied
	}
	if err = r.token.inspect(ctx, r.created, r.ops.guard); err != nil || !r.live(ctx) {
		return nil, ErrDenied
	}
	d, err := r.lifecycle.sources.accounts.disable(ctx, r.created)
	if err != nil || !r.lifecycle.accountReceipt(d, disabled) || !r.live(ctx) {
		return nil, ErrCleanup
	}
	if err = r.credential.Close(ctx); err != nil || !r.live(ctx) {
		return nil, ErrCleanup
	}
	clearRuntimeSecret(r.secret)
	r.target = r.ops.target()
	if r.target == nil {
		return nil, ErrUnconfigured
	}
	p, err = r.target.prepare(ctx, r.created, r.token.value(), spec, r.ops.guard)
	r.prepared.Store(p)
	if err != nil || p == nil || !r.live(ctx) {
		if p != nil {
			_ = p.Revoke()
		}
		return p, ErrDenied
	}
	return p, nil
}
