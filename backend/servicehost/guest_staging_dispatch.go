package servicehost

import (
	"context"
	"crypto/rand"
)

func (r *previewConfirmation) prepareGuestStaging() (string, error) {
	var ns GuestStagingNamespace
	if _, err := rand.Read(ns.Epoch[:]); err != nil {
		return r.failGrant(err)
	}
	if _, err := rand.Read(ns.Intent[:]); err != nil {
		return r.failGrant(err)
	}
	ns.Generation = 1 // independent single-use guest journal namespace, not issuer counter
	s := r.service
	s.mu.Lock()
	if !r.liveLockedNow() || !s.executionClosed || s.staging != nil || ns.Epoch == ([32]byte{}) || ns.Intent == ([32]byte{}) {
		s.mu.Unlock()
		return r.failGrant(ErrProvisioningRejected)
	}
	stage := &guestStagingRecord{service: s, owner: s.guestOwner, confirmation: r, descriptor: GuestStagingDescriptor{Preview: s.preview.descriptor, Namespace: ns}}
	s.staging = stage
	// The later concrete Endpoint composition supplies the pre-Split lease. No
	// pointer/boolean or absent native producer may open this path meanwhile.
	if r.request.endpoint != nil {
		stage.lease = r.request.endpoint.inspection
	}
	valid := r.request.authenticatedEndpointLocked()
	if valid {
		stage.transferred = true
	}
	s.mu.Unlock()
	if !valid {
		return "SERVICE_NOT_READY", nil
	}
	if err := callGuestPrepare(r.ctx, stage); err != nil {
		return r.failGrant(err)
	}
	return "SERVICE_NOT_READY", nil // disabled staging never generates runtime proof/grant
}
func callGuestPrepare(ctx context.Context, r *guestStagingRecord) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return r.owner.PrepareNeverCreatedGuest(ctx, GuestStagingTicket{r}, r.lease)
}
func (s *Service) reclaimGuestStaging(ctx context.Context) error {
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	r := s.staging
	if r == nil {
		s.mu.Unlock()
		return nil
	}
	if !r.matchesLocked(r.owner) || !s.stopping || !r.confirmation.finished || !s.reservation.finished || !s.workload.sameWorkloadLocked(s) || !s.workload.hostJoined {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	closed := r.closed
	s.mu.Unlock()
	if !closed {
		err := authCleanupCall(func() error { return r.owner.CloseNeverCreatedGuest(ctx, GuestStagingTicket{r}, r.lease) })
		if err != nil || !authBudget(ctx) {
			err = authFailure(err, ErrRuntimeUnconfirmed)
			if authFatal(err) {
				s.mu.Lock()
				_ = s.authWriter.FatalWorkloadLocked(err)
				s.mu.Unlock()
			}
			return err
		}
		s.mu.Lock()
		r.closed = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.staging != r || !r.closed || !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	return s.authWriter.ConfirmWorkloadLocked()
}
