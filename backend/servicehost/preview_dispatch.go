package servicehost

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// Only authenticated, sequence-checked dispatch calls this method. The resulting
// record is a display candidate, entirely separate from workload owner intents.
// Entropy is acquired before Service.mu; stop and preview commit share that gate.
func (s *Service) preparePreviewIntent(ctx context.Context, d *serviceDispatcher) (*servicecontrol.UntrustedIntent, string) {
	s.mu.Lock()
	configured := s.preview != nil
	s.mu.Unlock()
	if !configured {
		return nil, "SERVICE_UNCONFIGURED"
	}
	id, entropyErr := previewRandomHex(16)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preparePreviewIntentLocked(ctx, d, id, entropyErr, time.Now())
}

// now is display-state time only. Production supplies real time under the stop
// gate; authentication and request deadlines always keep their actual clocks.
func (s *Service) preparePreviewIntentLocked(ctx context.Context, d *serviceDispatcher, id string, entropyErr error, now time.Time) (*servicecontrol.UntrustedIntent, string) {
	p := s.preview
	if p == nil {
		return nil, "SERVICE_UNCONFIGURED"
	}
	if s.dispatch != d || d.service != s || s.issuer == nil || s.issuer != d.issuer || !s.authWriter.MatchesLocked(s.authOwner, s.issuer) {
		return nil, "SERVICE_STALE_CONTEXT"
	}
	if s.stopping || s.state != Running || !s.authWriter.OpenLocked() {
		return nil, "SERVICE_STOPPING"
	}
	if !authBudget(ctx) {
		return nil, "SERVICE_STALE_CONTEXT"
	}
	if s.ownerIntent != nil || s.reservation != nil {
		return nil, "SERVICE_NOT_READY"
	}
	s.invalidatePreviewLocked()
	if !now.Before(p.descriptor.HardDeadline) || p.descriptor.HardDeadline.UnixMilli() <= now.UnixMilli() {
		return nil, "SERVICE_INTENT_EXPIRED"
	}
	if entropyErr != nil {
		return nil, "SERVICE_INTERNAL_FAILURE"
	}
	if _, reused := p.seen[id]; reused {
		return nil, "SERVICE_INTERNAL_FAILURE"
	}
	snapshot, observed := s.issuer.TrySnapshot()
	if !observed {
		return nil, "SERVICE_NOT_READY"
	}
	if snapshot.Closed || snapshot.Generation != 0 || snapshot.HasAuthority || hex.EncodeToString(snapshot.InstanceEpoch[:]) != d.boot {
		return nil, "SERVICE_STALE_CONTEXT"
	}
	expires := now.Add(p.ttl)
	if p.descriptor.HardDeadline.Before(expires) {
		expires = p.descriptor.HardDeadline
	}
	if expires.UnixMilli() <= now.UnixMilli() {
		return nil, "SERVICE_INTENT_EXPIRED"
	}
	candidate := previewWire(p.descriptor, d.boot, id)
	digest, err := servicecontrol.PreviewDigest(candidate)
	if err != nil {
		return nil, "SERVICE_INTERNAL_FAILURE"
	}
	p.intent = &previewIntent{owner: s, dispatcher: d, id: id, digest: digest, expires: expires}
	p.seen[id] = struct{}{}
	return &servicecontrol.UntrustedIntent{IntentID: id, PreviewDigest: digest, IntentExpiresUnixMs: expires.UnixMilli(), Preview: candidate}, ""
}

// Display records never enter the workload reservation/approval state machine.
// The caller holds the same Service gate used to commit stop and issuer revoke.
func (s *Service) invalidatePreviewLocked() {
	if s.preview != nil && s.preview.intent != nil {
		s.preview.intent.invalidated = true
	}
}
