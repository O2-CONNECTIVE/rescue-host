package servicehost

import (
	"context"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

type runtimeKernelModel func(context.Context, PreviewConfirmationTicket) (*confirmationRuntimeProof, error)

func (f runtimeKernelModel) Acquire(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
	return f(ctx, ticket)
}

func (f runtimeKernelModel) CloseOwned(context.Context, *confirmationEvidenceOwner) error { return nil }

// This models an OS capability returned only after runtime inspection. It is not
// a native provider, staging receipt, operator authentication, or Windows proof.
func grantRuntimeFixture(t *testing.T, s *Service, targets ...*confirmationTarget) *confirmationRuntimeSource {
	t.Helper()
	source := &confirmationRuntimeSource{}
	source.self = source
	if len(targets) == 1 {
		source.target = targets[0]
	}
	owner := &confirmationEvidenceOwner{source: source, service: s, issuer: s.issuer, auth: s.authOwner, outer: s.outerTicket, expectedSID: "S-1-5-21-7-8-9-2048", proofs: make(map[*confirmationRuntimeProof]struct{})}
	owner.self, source.owner = owner, owner
	owner.target = source.target
	source.kernel = runtimeKernelModel(func(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
		descriptor, err := ticket.PreviewFor(s)
		if err != nil {
			return nil, err
		}
		proof := &confirmationRuntimeProof{source: source, owner: owner, confirmation: ticket.record, service: s, issuer: s.issuer, auth: s.authOwner, outer: s.outerTicket, descriptor: descriptor, stagingSID: owner.expectedSID, runtimeSID: owner.expectedSID, boundary: supervisor.SeparatedNonAdminGuest}
		proof.self = proof
		// This explicit package fake models independent same-target nonadmin evidence.
		// Inspection continuity never supplies this boundary in production.
		proof.target = source.target
		owner.mu.Lock()
		owner.proofs[proof] = struct{}{}
		owner.mu.Unlock()
		return proof, nil
	})
	s.mu.Lock()
	s.preview.runtime = source
	s.mu.Unlock()
	return source
}

func TestConfirmedRuntimeGrantTraversesRealPrepareStatusAndRevoke(t *testing.T) {
	var s *Service
	s, a, _ := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
		_, err := ticket.PreviewFor(s) // original owner Record recheck model
		return err
	})
	grantRuntimeFixture(t, s)
	hello := previewExchange(t, a, dispatchHello)
	prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	const approvalID = "33333333333333333333333333333333"
	approved := previewExchange(t, a, confirmationFrame(hello, prepared.Intent, 2, approvalID))
	if approved.Result != "approval-accepted" || approved.Approval == nil || approved.Approval.ApprovalRequestID != approvalID {
		t.Fatal("confirmed runtime capability did not reach real binding producer", approved)
	}
	status := previewExchange(t, a, dispatchRequest(hello, 3, "44444444444444444444444444444444", servicecontrol.Status))
	if status.Snapshot == nil || status.Snapshot.Generation != 1 || status.Snapshot.Binding == nil || status.Snapshot.Binding.Generation != 1 || status.Snapshot.LaunchPhase != "binding-issued" || status.Snapshot.ApprovalRequestID == nil || *status.Snapshot.ApprovalRequestID != approvalID || status.Snapshot.WorkloadCleanupConfirmed {
		t.Fatal("status did not retain the actual issued binding", status)
	}
	if actual := s.issuer.Snapshot(); actual.Generation != 1 || actual.HasAuthority || actual.Consumed || actual.Closed {
		t.Fatal("Prepare was skipped, launched, or revoked", actual)
	}
	quiesced := previewExchange(t, a, dispatchRequest(hello, 4, "55555555555555555555555555555555", servicecontrol.RevokeAllAndQuiesce))
	if quiesced.Quiesce == nil || !quiesced.Quiesce.RevocationApplied || quiesced.Quiesce.WorkloadCleanupConfirmed || !s.issuer.Snapshot().Closed || s.Snapshot().HasAuthority || s.host != nil || s.cfg.Launcher != nil {
		t.Fatal("revoke did not close the canonical issuer without launching", quiesced)
	}
}
