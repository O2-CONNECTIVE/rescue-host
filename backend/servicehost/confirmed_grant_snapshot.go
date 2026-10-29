package servicehost

import (
	"context"
	"strconv"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// Only an already authenticated channel may observe its retained actual Prepare
// result. Neither a positive counter nor an unheld binding creates this cache.
func (s *Service) confirmedGrantSnapshotLocked(d *serviceDispatcher, actual ephemeral.Snapshot) *servicecontrol.UntrustedSnapshot {
	if s.preview == nil || s.preview.grant == nil || actual.Generation == 0 || actual.Closed || actual.Consumed || actual.HasAuthority {
		return nil
	}
	g := s.preview.grant
	r := g.confirmation
	if r == nil || !r.finished || s.reservation == nil || s.preview.intent != r.intent || r.runtimeSource != s.preview.runtime || r.service != s || r.dispatcher != d || s.dispatch != d || r.issuer != s.issuer || d.issuer != r.issuer || r.owner != s.authOwner || r.grant != g || g.challenge.Binding() != g.binding || g.binding.Generation != actual.Generation || r.work.reservation != s.reservation || s.ownerIntent != s.reservation.intent || !s.reservation.finished {
		return nil
	}
	epoch, err := strconv.ParseUint(g.binding.Identity.Epoch, 10, 64)
	if err != nil {
		return nil
	}
	id := g.approvalID
	return &servicecontrol.UntrustedSnapshot{ServicePhase: string(s.state), Generation: actual.Generation, ApprovalRequestID: &id, LaunchPhase: "binding-issued", Binding: &servicecontrol.UntrustedBinding{Generation: g.binding.Generation, Identity: servicecontrol.UntrustedIdentity{LaunchID: g.binding.Identity.LaunchID, Epoch: epoch, PolicyDigest: g.binding.Identity.PolicyDigest}}}
}

func (s *Service) hasConfirmationRuntimeLocked() bool {
	return s.preview != nil && s.preview.intent != nil && s.preview.intent.confirmation != nil && s.preview.intent.confirmation.runtimeSource != nil
}

// The SCM owner joins grant/reservation/Host work before this RPC, then publishes
// whole-workload completion before joining auth workers that may await it.
// CloseOwned must join only its own guest/query/lease resources; it must not wait
// for auth join, Owner.AwaitWorkload, Plane.Close, or outer finalization.
func (s *Service) reclaimConfirmationRuntime(ctx context.Context) error {
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	if !s.hasConfirmationRuntimeLocked() {
		s.mu.Unlock()
		return nil
	}
	r := s.preview.intent.confirmation
	source, owner := r.runtimeSource, r.runtimeOwner
	if !s.stopping || r.service != s || r.issuer != s.issuer || r.owner != s.authOwner || !r.finished || s.reservation == nil || s.reservation != r.work.reservation || !s.reservation.finished || !s.workload.sameWorkloadLocked(s) || !s.workload.hostJoined || source != s.preview.runtime || owner == nil || owner != source.owner || source.kernel == nil {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	if r.runtimeCleanupFatal != nil {
		err := r.runtimeCleanupFatal
		s.mu.Unlock()
		return err
	}
	cleaned := r.runtimeCleanupConfirmed
	s.mu.Unlock()
	if !cleaned {
		// Canonical issuance is irrevocably closed before potentially blocked OS
		// cleanup, including nil/error/panic Acquire with only the actor retained.
		if actual, ok := r.issuer.TrySnapshot(); !ok || !actual.Closed {
			return ErrRuntimeUnconfirmed
		}
		err := authCleanupCall(func() error { return source.kernel.CloseOwned(ctx, owner) })
		if err != nil || !authBudget(ctx) {
			err = authFailure(err, ErrRuntimeUnconfirmed)
			if authFatal(err) {
				s.mu.Lock()
				r.runtimeCleanupFatal = err
				// Fatal is a retained failure, never workload success. Wake helper
				// cleanup waiting on this same owner so the original auth worker
				// can retire without awaiting a success ack that cannot be issued.
				if s.preview.intent.confirmation == r && s.preview.runtime == source && r.runtimeOwner == owner && s.workload.sameWorkloadLocked(s) {
					_ = s.authWriter.FatalWorkloadLocked(err)
				}
				s.mu.Unlock()
			}
			return err
		}
		s.mu.Lock()
		r.runtimeCleanupConfirmed = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !authBudget(ctx) || !s.stopping || s.preview.intent.confirmation != r || s.preview.runtime != source || r.runtimeOwner != owner || !s.workload.sameWorkloadLocked(s) || !s.workload.hostJoined || !r.runtimeCleanupConfirmed {
		return ErrRuntimeUnconfirmed
	}
	if s.authWriter.ConfirmWorkloadLocked() != nil {
		return ErrRuntimeUnconfirmed
	}
	s.authWriter.NotifyPendingLocked()
	return nil
}
