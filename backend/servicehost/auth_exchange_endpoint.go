package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// Only the private memory seam replaces operations. Production wraps the exact
// concrete Endpoint and never manufactures its cross-package receipts.
type serviceAuthEndpointOps struct {
	concrete  *serviceready.Endpoint
	factories *serviceNativeFactories
	close     func(context.Context) error
}
type serviceAuthEndpoint struct {
	ops                           serviceAuthEndpointOps
	closed                        *serviceready.EndpointClosed
	written                       *serviceready.WriteReceipt
	inspection                    *serviceready.InspectionLease
	input                         *serviceready.SoleInput
	inspectionClose               func(context.Context) error // private physical-OS seam; production uses exact lease.Close
	inspectionReturned            bool                        // actual physical close ack; independent of whole guest teardown
	acquiring, uncertain, cleaned bool                        // Service.mu
}

func (r *serviceAuthRequest) authenticatedEndpointLocked() bool {
	return r.batchConsumed && r.endpointInputBoundLocked()
}

func (r *serviceAuthRequest) endpointInputBoundLocked() bool {
	held := r.endpoint
	return r.endpointExchange && held != nil && held.ops.concrete != nil && held.ops.factories.canonical() && held.inspection != nil && held.input != nil && r.batch.concrete != nil && r.batch.concrete.MatchesSoleInput(held.input) && r.batch.concrete.MatchesServiceFactory(held.ops.factories.auth)
}

// prepareEndpoint is ownership preparation only. The protected expected
// operator source remains unconfigured; ownership capture authenticates no Read.
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

// A transferred inspection lease belongs to the original execution owner until
// its actual guest/query cleanup acknowledgement. Caller cancellation is not one.
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
	inspection := held.inspection
	guestOwnsInspection := s.staging != nil && s.staging.lease == inspection && s.staging.transferred && !s.staging.prepareJoined && !s.staging.closed || s.runtimeGuestOwnsInspectionLocked(inspection)
	inspectionReturned, closeInspection := held.inspectionReturned, held.inspectionClose
	s.mu.Unlock()
	if acquiring {
		return ErrRuntimeUnconfirmed
	}
	if inspection != nil && !inspectionReturned {
		if guestOwnsInspection {
			return ErrRuntimeUnconfirmed
		}
		if closeInspection == nil {
			closeInspection = inspection.Close
		}
		if err := authCleanupCall(func() error { return closeInspection(ctx) }); err != nil {
			return authFailure(err, ErrRuntimeUnconfirmed)
		}
		// Retain the actual physical fact even if this caller's timer expires
		// afterwards. It is not a whole-guest or authenticated exchange proof.
		s.mu.Lock()
		held.inspectionReturned = true
		s.mu.Unlock()
		if !authBudget(ctx) {
			return ErrRuntimeUnconfirmed
		}
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

// The same worker holds partial returns before error/cancel inspection.
// Lease acquisition precedes Split while the connected duplex still exists.
func (r *serviceAuthRequest) splitEndpointForInspection(ctx context.Context) (*serviceready.SoleInput, error) {
	if r == nil || r.adapter == nil {
		return nil, ErrProvisioningRejected
	}
	s := r.adapter.service
	s.mu.Lock()
	held := r.endpoint
	if held == nil || held.ops.concrete == nil || held.input != nil || held.inspection != nil || !r.acquisitionLiveLocked(ctx) {
		s.mu.Unlock()
		return nil, ErrProvisioningRejected
	}
	endpoint := held.ops.concrete
	s.mu.Unlock()
	if err := endpoint.AwaitConnected(ctx); err != nil {
		return nil, authFailure(err, ErrProvisioningRejected)
	}
	input, lease, err := endpoint.SplitForInspection(ctx)
	s.mu.Lock()
	held.input, held.inspection = input, lease
	if lease != nil {
		held.inspectionClose = lease.Close
	}
	live := r.acquisitionLiveLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		return input, authFailure(err, ErrProvisioningRejected)
	}
	if input == nil || lease == nil || !live {
		return input, ErrProvisioningRejected
	}
	return input, nil
}

// No caller is configured until an independent protected expected operator
// policy supplies this concrete factory. Nil never adopts observed candidates.
func (a *serviceAuthAdapter) exchangeControlEndpoint(ctx context.Context) error {
	if a == nil || a.service == nil {
		return ErrProvisioningRejected
	}
	s := a.service
	s.mu.Lock()
	control := s.control
	var factories *serviceNativeFactories
	if control != nil {
		factories = control.factories
	}
	s.mu.Unlock()
	if !factories.canonical() || !factories.batch.MatchesServiceFactory(factories.auth) || !factories.batch.RuntimeConfigured() {
		return ErrProvisioningUnconfigured
	}
	r, err := a.reserveExchange(ctx)
	if err != nil {
		return err
	}
	return r.runOwner(func() error {
		if _, err := r.prepareControlEndpoint(r.worker); err != nil {
			return err
		}
		input, err := r.splitEndpointForInspection(r.worker)
		if err != nil {
			return err
		}
		batch, err := factories.batch.Prepare(input, r.owner, r.issuer)
		if batch != nil {
			attachErr := r.attachBatch(batch)
			if err == nil {
				err = attachErr
			}
		}
		if err != nil {
			return authFailure(err, ErrProvisioningRejected)
		}
		if batch == nil {
			return ErrProvisioningUnconfigured
		}
		return r.exchangeBody(nil)
	})
}
