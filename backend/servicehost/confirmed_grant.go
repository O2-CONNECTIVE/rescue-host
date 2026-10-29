package servicehost

import (
	"context"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// There is no product constructor/registration for this OS seam. A future native
// adapter must return a held runtime capability, never a static staging report.
// Only package memory fixtures currently construct a source or runtime proof.
type confirmationRuntimeKernel interface {
	Acquire(context.Context, PreviewConfirmationTicket) (*confirmationRuntimeProof, error)
	// CloseOwned joins this actor's guest/query/lease work only. It must not
	// await auth workers, Owner.AwaitWorkload, Plane.Close or outer finalization.
	CloseOwned(context.Context, *confirmationEvidenceOwner) error
}
type confirmationRuntimeSource struct {
	self   *confirmationRuntimeSource
	kernel confirmationRuntimeKernel
	owner  *confirmationEvidenceOwner
}
type confirmationEvidenceOwner struct {
	mu          sync.Mutex // evidence gate -> actual issuer only; never Service.mu
	self        *confirmationEvidenceOwner
	source      *confirmationRuntimeSource
	service     *Service
	issuer      *ephemeral.Issuer
	auth        *serviceauthowner.Owner
	outer       *innerCleanupTicket
	expectedSID string // immutable actual creation receipt, not the initial auth profile
	revoked     bool
	proofs      map[*confirmationRuntimeProof]struct{} // retained by the original OS owner
}
type confirmationRuntimeProof struct {
	self         *confirmationRuntimeProof
	source       *confirmationRuntimeSource
	owner        *confirmationEvidenceOwner
	confirmation *previewConfirmation
	service      *Service
	issuer       *ephemeral.Issuer
	auth         *serviceauthowner.Owner
	outer        *innerCleanupTicket
	descriptor   PreviewDescriptor
	stagingSID   string
	runtimeSID   string
	boundary     supervisor.GuestBoundary
	consumed     bool // evidence owner gate
}

type confirmedGrant struct {
	confirmation *previewConfirmation
	challenge    ephemeral.Challenge
	binding      supervisor.Binding
	approvalID   string
}

func (o *confirmationEvidenceOwner) revoke() error {
	if o == nil || o.self != o || o.issuer == nil || o.auth == nil {
		return ErrRuntimeUnconfirmed
	}
	o.mu.Lock()
	o.revoked = true
	err := o.issuer.Close() // commit canonical revocation before any worker cancellation
	o.mu.Unlock()
	if err != nil {
		return ErrRuntimeUnconfirmed
	}
	return o.auth.Revoke() // no evidence/issuer gate is held while entering Service.mu
}

func (r *previewConfirmation) prepareConfirmedGrant(approvalID string) (string, error) {
	s, source := r.service, r.runtimeSource
	s.mu.Lock()
	closed := s.executionClosed
	s.mu.Unlock()
	if closed {
		return r.failGrant(ErrProvisioningRejected)
	}
	if source == nil || source.self != source || source.kernel == nil {
		return "SERVICE_NOT_READY", nil
	}
	proof, err := acquireConfirmationRuntime(r.request.worker, source.kernel, PreviewConfirmationTicket{record: r})
	// Preserve every returned partial reference before inspecting cancellation/error.
	s.mu.Lock()
	r.runtimeProof = proof
	if proof != nil {
		r.partialOwner = proof.owner
	}
	live := r.liveLocked(time.Now())
	descriptor := s.preview.descriptor
	descriptor.AllowedKeyFingerprints = append([]string(nil), descriptor.AllowedKeyFingerprints...)
	s.mu.Unlock()
	if err != nil || !live {
		return r.failGrant(err)
	}
	challenge, err := r.consumeRuntimeProof(proof, descriptor)
	if err != nil {
		return r.failGrant(err)
	}
	s.mu.Lock()
	// Real Prepare has already consumed the actual generation. Never roll it back.
	r.grant = &confirmedGrant{confirmation: r, challenge: challenge, binding: challenge.Binding(), approvalID: approvalID}
	live = r.liveLocked(time.Now())
	if live {
		s.preview.grant = r.grant
	}
	s.mu.Unlock()
	if !live {
		return r.failGrant(ErrRuntimeUnconfirmed)
	}
	return "", nil
}

func (r *previewConfirmation) consumeRuntimeProof(p *confirmationRuntimeProof, d PreviewDescriptor) (ephemeral.Challenge, error) {
	o, source := r.runtimeOwner, r.runtimeSource
	if o == nil || o.self != o || !o.mu.TryLock() {
		return ephemeral.Challenge{}, ErrProvisioningRejected
	}
	defer o.mu.Unlock()
	if o.revoked || o.source != source || source.owner != o || o.service != r.service || o.issuer != r.issuer || o.auth != r.owner || o.outer != r.service.outerTicket || !validRuntimeGuestSID(o.expectedSID) || slices.Contains(r.service.preview.excludedSIDs, o.expectedSID) {
		return ephemeral.Challenge{}, ErrProvisioningRejected
	}
	if p == nil || p.self != p || p.source != source || p.owner != o || p.confirmation != r || p.service != r.service || p.issuer != r.issuer || p.auth != r.owner || p.outer != o.outer || p.consumed || p.stagingSID != o.expectedSID || p.runtimeSID != o.expectedSID || p.boundary != supervisor.SeparatedNonAdminGuest || !sameGrantDescriptor(p.descriptor, d) {
		return ephemeral.Challenge{}, ErrProvisioningRejected
	}
	if _, held := o.proofs[p]; !held {
		return ephemeral.Challenge{}, ErrProvisioningRejected
	}
	if !authBudget(r.ctx) || !authBudget(r.request.worker) || !time.Now().Before(r.intent.expires) || !time.Now().Before(d.HardDeadline) || d.LeaseCeiling <= 0 || !time.Now().Add(d.LeaseCeiling).Before(d.HardDeadline) {
		return ephemeral.Challenge{}, ErrProvisioningRejected
	}
	p.consumed = true
	return r.issuer.Prepare(ephemeral.ApprovedPolicy{PolicyDigest: hex.EncodeToString(d.PolicyDigest[:]), KeySetDigest: d.KeySetDigest, GuestBoundary: p.boundary, HardDeadline: d.HardDeadline, BackendLeaseCeiling: d.LeaseCeiling})
}

func (r *previewConfirmation) failGrant(err error) (string, error) {
	err = authFailure(err, ErrRuntimeUnconfirmed)
	r.service.stopFor(err)
	return "", err
}
func acquireConfirmationRuntime(ctx context.Context, k confirmationRuntimeKernel, t PreviewConfirmationTicket) (p *confirmationRuntimeProof, err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return k.Acquire(ctx, t)
}
func sameGrantDescriptor(a, b PreviewDescriptor) bool {
	return a.ConfigDigest == b.ConfigDigest && a.PolicyDigest == b.PolicyDigest && a.KeySetDigest == b.KeySetDigest && a.PolicyMode == b.PolicyMode && a.HardDeadline == b.HardDeadline && a.LeaseCeiling == b.LeaseCeiling && slices.Equal(a.AllowedKeyFingerprints, b.AllowedKeyFingerprints)
}
func validRuntimeGuestSID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 8 || strings.Join(parts[:4], "-") != "S-1-5-21" {
		return false
	}
	for _, part := range parts[4:] {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}
