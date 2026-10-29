package servicehost

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// writeDispatchReply accepts only the exact dispatcher reply retained by this
// authenticated exchange. The legacy private fixture writer is not native
// evidence; concrete Endpoint writes keep the actual opaque receipt separately.
func (r *serviceAuthRequest) writeDispatchReply(reply *serviceDispatchReply, legacy func(context.Context, []byte) error) error {
	if r == nil || r.adapter == nil {
		return ErrProvisioningRejected
	}
	s := r.adapter.service
	s.mu.Lock()
	if r.endpointExchange && !r.authenticatedEndpointLocked() {
		s.mu.Unlock()
		return ErrProvisioningUnconfigured
	}
	valid := reply != nil && r.reply == reply && reply.owner == r.adapter.dispatch && r.adapter.active == r && s.auth == r.adapter && r.batchConsumed && r.ownerRunning && !r.finished && !r.finalizing && authBudget(r.request) && authBudget(reply.ctx)
	held := r.endpoint
	s.mu.Unlock()
	if !valid {
		return ErrProvisioningRejected
	}
	if held == nil {
		if legacy == nil {
			return ErrProvisioningUnconfigured
		}
		return legacy(r.request, append([]byte(nil), reply.frame...))
	}
	if held.ops.concrete == nil {
		return ErrProvisioningUnconfigured
	}
	// This response was admitted by dispatch.begin. Preserve the existing terminal
	// quiesce-response ordering; it does not grant any new Start after revocation.
	receipt, err := held.ops.concrete.WriteResponse(r.request, reply.frame)
	if err != nil || receipt == nil || !authBudget(r.request) {
		return authFailure(err, ErrRuntimeUnconfirmed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.endpoint != held || r.reply != reply || r.finalizing || r.finished {
		return ErrRuntimeUnconfirmed
	}
	held.written = receipt
	return nil
}

// Only the privately owned same-profile pair can prepare a controlled Endpoint.
// The real prepare/move/Batch provenance path is still not an authenticated Start.
func (r *serviceAuthRequest) prepareControlEndpoint(ctx context.Context) (*serviceready.Endpoint, error) {
	if r == nil || r.adapter == nil {
		return nil, ErrProvisioningRejected
	}
	s := r.adapter.service
	s.mu.Lock()
	control := s.control
	var factories *serviceNativeFactories
	if control != nil {
		factories = control.factories
	}
	s.mu.Unlock()
	if !factories.canonical() {
		return nil, ErrProvisioningUnconfigured
	}
	if err := r.observeControl(ctx, controlBeforeAcquire); err != nil {
		return nil, err
	}
	return r.prepareEndpointOps(ctx, func(c context.Context) (serviceAuthEndpointOps, error) {
		endpoint, err := factories.prepareEndpoint(c)
		ops := r.nativeEndpointOperations(endpoint)
		ops.factories = factories
		return ops, err
	})
}
