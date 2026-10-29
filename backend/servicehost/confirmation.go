package servicehost

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// PreviewConfirmationResolver belongs to the already bound outer owner. It
// rechecks protected configuration there; it creates no approval or launch grant.
type PreviewConfirmationResolver interface {
	ResolvePreviewConfirmation(context.Context, PreviewConfirmationTicket) error
}

// PreviewConfirmationTicket is a live display observation, not authentication,
// guest proof, approval, or authority. A zero ticket cannot observe any Service.
type PreviewConfirmationTicket struct{ record *previewConfirmation }

type previewConfirmation struct {
	service                 *Service
	dispatcher              *serviceDispatcher
	issuer                  *ephemeral.Issuer
	owner                   *serviceauthowner.Owner
	intent                  *previewIntent
	request                 *serviceAuthRequest
	ctx                     context.Context
	runtimeSource           *confirmationRuntimeSource
	runtimeOwner            *confirmationEvidenceOwner
	partialOwner            *confirmationEvidenceOwner
	runtimeProof            *confirmationRuntimeProof
	runtimeCleanupConfirmed bool
	runtimeCleanupFatal     error
	work                    serviceReservationWork
	grant                   *confirmedGrant
	finished                bool // Service.mu; set only after the resolver actually returns
}

// PreviewFor returns a clone only while this exact request and consumed intent
// remain live. Contention refuses observation rather than waiting past its budget.
func (t PreviewConfirmationTicket) PreviewFor(s *Service) (PreviewDescriptor, error) {
	r := t.record
	if s == nil || r == nil || r.service != s || !authBudget(r.ctx) || !s.mu.TryLock() {
		return PreviewDescriptor{}, ErrProvisioningRejected
	}
	defer s.mu.Unlock()
	if !r.liveLocked(time.Now()) {
		return PreviewDescriptor{}, ErrProvisioningRejected
	}
	descriptor := s.preview.descriptor
	descriptor.AllowedKeyFingerprints = append([]string(nil), descriptor.AllowedKeyFingerprints...)
	if !authBudget(r.ctx) {
		return PreviewDescriptor{}, ErrProvisioningRejected
	}
	return descriptor, nil
}

func (r *previewConfirmation) liveLocked(now time.Time) bool {
	s := r.service
	if r.finished || s.preview == nil || s.preview.intent != r.intent || r.intent.confirmation != r || r.intent.invalidated || r.intent.owner != s || r.intent.dispatcher != r.dispatcher || s.dispatch != r.dispatcher || r.dispatcher.service != s || s.issuer != r.issuer || r.dispatcher.issuer != r.issuer || s.authOwner != r.owner || !s.authWriter.MatchesLocked(r.owner, r.issuer) || s.stopping || s.state != Running || !authBudget(r.ctx) || !now.Before(r.intent.expires) || now.UnixMilli() >= r.intent.expires.UnixMilli() || !now.Before(s.preview.descriptor.HardDeadline) {
		return false
	}
	if s.auth == nil || s.auth.active != r.request || r.request == nil || !r.request.liveLocked() {
		return false
	}
	if r.runtimeSource != nil && (r.work.reservation == nil || s.reservation != r.work.reservation || s.ownerIntent != r.work.reservation.intent || !s.reservation.claimed || s.reservation.canceled || s.reservation.finished) {
		return false
	}
	snapshot, observed := r.issuer.TrySnapshot()
	if !observed || snapshot.Closed || snapshot.HasAuthority || hex.EncodeToString(snapshot.InstanceEpoch[:]) != r.dispatcher.boot || !authBudget(r.ctx) {
		return false
	}
	if r.grant == nil {
		return snapshot.Generation == 0
	}
	return r.grant.confirmation == r && r.grant.challenge.Binding() == r.grant.binding && r.grant.binding.Generation == snapshot.Generation && !snapshot.Consumed
}

// Called only after authenticated read, boot/channel/sequence/replay validation.
// The consumed display record remains owned by the existing auth worker until
// the resolver's real return; no detached resolver worker or cleanup ack is made.
func (s *Service) confirmPreviewIntent(ctx context.Context, d *serviceDispatcher, request servicecontrol.UntrustedRequest) (string, error) {
	s.mu.Lock()
	p := s.preview
	if p == nil || p.resolver == nil {
		s.mu.Unlock()
		return "SERVICE_UNCONFIGURED", nil
	}
	if s.stopping || s.state != Running {
		s.mu.Unlock()
		return "SERVICE_STOPPING", nil
	}
	i := p.intent
	if s.dispatch != d || d.service != s || s.issuer == nil || s.issuer != d.issuer || request.BootEpoch != d.boot || request.ChannelNonce != d.profile.ChannelNonce || i == nil || i.owner != s || i.dispatcher != d || i.invalidated || i.id != request.Body.IntentID || i.digest != request.Body.PreviewDigest || !authBudget(ctx) {
		s.mu.Unlock()
		return "SERVICE_STALE_CONTEXT", nil
	}
	now := time.Now()
	if !now.Before(i.expires) || now.UnixMilli() >= i.expires.UnixMilli() || !now.Before(p.descriptor.HardDeadline) {
		s.mu.Unlock()
		return "SERVICE_INTENT_EXPIRED", nil
	}
	if i.confirmation != nil {
		s.mu.Unlock()
		return "SERVICE_INTENT_CONSUMED", nil
	}
	if s.auth == nil || s.auth.active == nil {
		s.mu.Unlock()
		return "SERVICE_STALE_CONTEXT", nil
	}
	r := &previewConfirmation{service: s, dispatcher: d, issuer: s.issuer, owner: s.authOwner, intent: i, request: s.auth.active, ctx: ctx}
	i.confirmation = r
	if !r.liveLocked(now) {
		r.finished = true
		s.mu.Unlock()
		return "SERVICE_STALE_CONTEXT", nil
	}
	source := p.runtime // static owner reference; no acquisition responsibility yet
	if source != nil {
		if s.ownerIntent != nil || s.reservation != nil {
			r.finished = true
			s.mu.Unlock()
			return "SERVICE_NOT_READY", nil
		}
		s.ownerIntent = &serviceOwnerIntent{owner: s, issuer: s.issuer, expires: i.expires}
		reservation, err := s.reserveOwnerIntentLocked(ctx, s.ownerIntent)
		if err == nil {
			r.work, err = s.consumeOwnerReservationLocked(reservation)
		}
		if err != nil {
			r.finished = true
			s.mu.Unlock()
			s.stopFor(err)
			return "", ErrRuntimeUnconfirmed
		}
		// Take per-confirm actor responsibility only with a real claimed work
		// ticket. This remains before Record/Acquire, under the same stop gate.
		r.runtimeSource = source
		r.runtimeOwner = source.owner
	}
	resolver := p.resolver
	s.mu.Unlock()
	defer r.finishConfirmation()
	err := callPreviewConfirmation(ctx, resolver, PreviewConfirmationTicket{record: r})
	s.mu.Lock()
	live := r.liveLocked(time.Now())
	s.mu.Unlock()
	if err != nil || !live {
		err = authFailure(err, ErrRuntimeUnconfirmed)
		s.stopFor(err)
		return "", err
	}
	if r.runtimeSource == nil {
		return "SERVICE_NOT_READY", nil
	}
	return r.prepareConfirmedGrant(request.RequestID)
}

func callPreviewConfirmation(ctx context.Context, resolver PreviewConfirmationResolver, ticket PreviewConfirmationTicket) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return resolver.ResolvePreviewConfirmation(ctx, ticket)
}

func (r *previewConfirmation) finishConfirmation() {
	s := r.service
	s.mu.Lock()
	r.finished = true
	s.mu.Unlock()
	if r.work.ticket != nil {
		_ = s.finishOwnerReservation(r.work)
	}
}
