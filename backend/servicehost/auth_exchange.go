package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
)

// reserveExchange registers ownership before an Endpoint constructor can run.
// It grants no authentication, input provenance, or next-request readiness.
// Native SoleInput preparation and SCM witness turnover remain unconnected.
func (a *serviceAuthAdapter) reserveExchange(ctx context.Context) (*serviceAuthRequest, error) {
	if a == nil || a.service == nil || !authBudget(ctx) {
		return nil, ErrProvisioningRejected
	}
	s := a.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if !a.canReserveLocked(ctx) {
		return nil, ErrProvisioningRejected
	}
	r := a.reserveLocked(ctx)
	r.endpointExchange = true
	return r, nil
}
func (a *serviceAuthAdapter) canReserveLocked(ctx context.Context) bool {
	s := a.service
	return s.auth == a && s.dispatch == a.dispatch && s.state == Running && !s.stopping && s.issuer != nil && s.authWriter.OpenLocked() && s.authWriter.MatchesLocked(s.authOwner, s.issuer) && a.attempts < a.limit && a.previousAcknowledgedLocked() && authBudget(ctx)
}
func (a *serviceAuthAdapter) reserveLocked(ctx context.Context) *serviceAuthRequest {
	worker, cancel := context.WithCancel(ctx)
	r := &serviceAuthRequest{adapter: a, issuer: a.service.issuer, owner: a.service.authOwner, request: ctx, worker: worker, cancel: cancel, done: make(chan struct{}), ownerDone: make(chan struct{}), cleanup: make(chan serviceCleanupRequest)}
	a.attempts++
	a.active = r
	return r
}
func authBatchOperationsValid(batch serviceAuthBatchOps) bool {
	return batch.collector != nil && batch.start != nil && batch.collect != nil && batch.consume != nil && batch.close != nil
}
func (a *serviceAuthAdapter) matchesBatchLocked(batch serviceAuthBatchOps) bool {
	s := a.service
	return batch.owner == s.authOwner && batch.issuer == s.issuer && s.authWriter.MatchesLocked(batch.owner, batch.issuer)
}
func (a *serviceAuthAdapter) retainBatchLocked(r *serviceAuthRequest, batch serviceAuthBatchOps) {
	r.batch = batch
	a.seen[batch.collector] = struct{}{}
}

// attachBatch verifies only issuer/Owner/Collector identity. The current Batch
// cannot prove which SoleInput it used. This does not open Endpoint Start.
func (r *serviceAuthRequest) attachBatch(batch *operatorauth.Batch) error {
	if r == nil || r.adapter == nil {
		return ErrProvisioningRejected
	}
	ops, err := nativeServiceAuthBatch(batch, r.issuer, r.owner)
	if err != nil {
		return err
	}
	return r.attachBatchOps(ops)
}

// Private memory seam, as with exchangeOps. Production wraps a concrete Batch.
func (r *serviceAuthRequest) attachBatchOps(batch serviceAuthBatchOps) error {
	if r == nil || r.adapter == nil || !authBatchOperationsValid(batch) {
		return ErrProvisioningRejected
	}
	a, s := r.adapter, r.adapter.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auth != a || a.active != r || r.finished || r.finalizing || r.ownerEndedLocked() || r.batch.collector != nil || r.issuer != s.issuer || r.owner != s.authOwner || !a.matchesBatchLocked(batch) {
		return ErrProvisioningRejected
	}
	if _, seen := a.seen[batch.collector]; seen {
		return ErrProvisioningRejected
	}
	// A same-owner constructor admitted before cancellation transfers its late
	// retained reference for cleanup, even though further Start is now rejected.
	a.retainBatchLocked(r, batch)
	if s.stopping || !s.authWriter.OpenLocked() || !authBudget(r.worker) {
		return ErrProvisioningRejected
	}
	return nil
}
func (r *serviceAuthRequest) ownerEndedLocked() bool {
	select {
	case <-r.ownerDone:
		return true
	default:
		return false
	}
}
