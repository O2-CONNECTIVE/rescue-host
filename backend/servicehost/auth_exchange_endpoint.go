package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// Only the private memory seam replaces operations. Production wraps the exact
// concrete Endpoint and never manufactures its cross-package receipts.
type serviceAuthEndpointOps struct {
	concrete *serviceready.Endpoint
	close    func(context.Context) error
}
type serviceAuthEndpoint struct {
	ops                           serviceAuthEndpointOps
	closed                        *serviceready.EndpointClosed
	written                       *serviceready.WriteReceipt
	inspection                    *serviceready.InspectionLease
	input                         *serviceready.SoleInput
	acquiring, uncertain, cleaned bool // Service.mu
}

func (r *serviceAuthRequest) authenticatedEndpointLocked() bool {
	return r.endpointExchange && r.batchConsumed && r.endpoint != nil && r.endpoint.ops.concrete != nil && r.endpoint.inspection != nil && r.endpoint.input != nil && r.batch.concrete != nil && r.batch.concrete.MatchesSoleInput(r.endpoint.input)
}

// prepareEndpoint is ownership preparation only. There is no configured caller
// until same-Service/profile binding, the Native typed-input factory, and SCM
// readiness composition exist. Ownership capture alone proves none of them.
func (r *serviceAuthRequest) prepareEndpoint(ctx context.Context, f *serviceready.Factory) (*serviceready.Endpoint, error) {
	if f == nil {
		return nil, ErrProvisioningUnconfigured
	}
	return r.prepareEndpointOps(ctx, func(c context.Context) (serviceAuthEndpointOps, error) {
		endpoint, err := f.PrepareEndpoint(c)
		return r.nativeEndpointOperations(endpoint), err
	})
}
func (r *serviceAuthRequest) acquisitionLiveLocked(ctx context.Context) bool {
	s, a := r.adapter.service, r.adapter
	return s.auth == a && a.active == r && r.issuer == s.issuer && r.owner == s.authOwner && s.authWriter.MatchesLocked(r.owner, r.issuer) && s.authWriter.OpenLocked() && !s.stopping && !r.finished && !r.ownerEndedLocked() && r.ownerRunning && !r.finalizing && authBudget(ctx) && authBudget(r.worker)
}
func (r *serviceAuthRequest) prepareEndpointOps(ctx context.Context, prepare func(context.Context) (serviceAuthEndpointOps, error)) (endpoint *serviceready.Endpoint, err error) {
	if r == nil || r.adapter == nil || prepare == nil {
		return nil, ErrProvisioningRejected
	}
	s := r.adapter.service
	s.mu.Lock()
	if !r.endpointExchange || r.endpoint != nil || !r.acquisitionLiveLocked(ctx) {
		s.mu.Unlock()
		return nil, ErrProvisioningRejected
	}
	held := &serviceAuthEndpoint{acquiring: true}
	r.endpoint = held
	s.mu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			s.mu.Lock()
			held.acquiring = false
			held.uncertain = true
			s.mu.Unlock()
			panic(p)
		}
	}()
	ops, acquireErr := prepare(ctx)
	s.mu.Lock()
	held.ops, held.acquiring = ops, false
	live := r.acquisitionLiveLocked(ctx)
	s.mu.Unlock()
	// Partial or canceled late owners remain in the original record. Never erase
	// them because a stop won while the constructor was already in flight.
	if acquireErr != nil {
		return ops.concrete, authFailure(acquireErr, ErrProvisioningRejected)
	}
	if ops.concrete == nil || ops.close == nil {
		return ops.concrete, ErrProvisioningUnconfigured
	}
	if !live {
		return ops.concrete, ErrProvisioningRejected
	}
	return ops.concrete, nil
}

// AwaitConnected/Split and typed SoleInput-to-Batch preparation are not connected
// here. A caller-supplied input pointer cannot establish that provenance.
func (r *serviceAuthRequest) closeEndpoint(ctx context.Context) error {
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s := r.adapter.service
	s.mu.Lock()
	held := r.endpoint
	if held == nil {
		s.mu.Unlock()
		return nil
	}
	cleaned, acquiring, uncertain, ops := held.cleaned, held.acquiring, held.uncertain, held.ops
	s.mu.Unlock()
	if acquiring {
		return ErrRuntimeUnconfirmed
	}
	if !cleaned && ops.concrete != nil {
		if ops.close == nil {
			return ErrRuntimeUnconfirmed
		}
		if err := authCleanupCall(func() error { return ops.close(ctx) }); err != nil || !authBudget(ctx) {
			return authFailure(err, ErrRuntimeUnconfirmed)
		}
		s.mu.Lock()
		held.cleaned = true
		s.mu.Unlock()
	}
	if uncertain {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (r *serviceAuthRequest) nativeEndpointOperations(endpoint *serviceready.Endpoint) serviceAuthEndpointOps {
	if endpoint == nil {
		return serviceAuthEndpointOps{}
	}
	return serviceAuthEndpointOps{concrete: endpoint, close: func(cleanup context.Context) error {
		receipt, err := endpoint.CloseEndpoint(cleanup)
		if err != nil {
			return err
		}
		if receipt == nil {
			return ErrRuntimeUnconfirmed
		}
		s := r.adapter.service
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.endpoint == nil || r.endpoint.ops.concrete != endpoint {
			return ErrRuntimeUnconfirmed
		}
		r.endpoint.closed = receipt
		return nil
	}}
}
