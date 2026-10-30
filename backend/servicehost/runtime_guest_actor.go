package servicehost

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// The wrapper is fixed before native acquisition; only its retained material is
// filled later. Source, evidence owner and cleanup target are never replaced.
type runtimeGuestActor struct {
	self     *runtimeGuestActor
	service  *Service
	owner    RuntimeGuestOwner
	kernel   runtimeGuestKernel
	source   *confirmationRuntimeSource
	record   *runtimeGuestRecord // Service.mu
	material atomic.Pointer[runtimeGuestMaterial]
	revoked  atomic.Bool
}

func (s *Service) bindRuntimeGuestLocked() error {
	if s.preview == nil || s.preview.runtimeGuest == nil {
		return nil
	}
	a := s.preview.runtimeGuest
	if a.self != a || a.service != s || !sameGuestOwner(s.outer, a.owner) || s.issuer == nil || s.authOwner == nil || s.guestOwner != nil || s.executionClosed || s.preview.runtime != nil || s.preview.targetOwner != nil || s.stopping {
		return ErrProvisioningRejected
	}
	target := &confirmationTarget{ops: a}
	source := &confirmationRuntimeSource{kernel: a, target: target}
	source.self = source
	o := &confirmationEvidenceOwner{source: source, service: s, issuer: s.issuer, auth: s.authOwner, outer: s.outerTicket, target: target, proofs: make(map[*confirmationRuntimeProof]struct{})}
	o.self, source.owner = o, o
	a.source = source
	s.preview.runtime = source
	s.preview.targetOwner = &confirmationTargetOwner{source: source, owner: o, kernel: a, target: target}
	return nil
}

func (a *runtimeGuestActor) inspect(ctx context.Context) error {
	m := a.material.Load()
	if m == nil || m.target == nil || a.revoked.Load() {
		return ErrProvisioningRejected
	}
	return m.target.inspect(ctx)
}
func (a *runtimeGuestActor) commit(ctx context.Context) error {
	m := a.material.Load()
	if m == nil || m.target == nil || a.revoked.Load() {
		return ErrProvisioningRejected
	}
	return m.target.commit(ctx)
}
func (a *runtimeGuestActor) revoke() error {
	a.revoked.Store(true)
	if m := a.material.Load(); m != nil {
		if m.target != nil {
			return m.target.revoke()
		}
		return m.prepared.Revoke()
	}
	return nil // no material has yet returned; a late reply observes the latch
}
func (a *runtimeGuestActor) close(ctx context.Context) error {
	if !authBudget(ctx) || !a.revoked.Load() {
		return ErrRuntimeUnconfirmed
	}
	if m := a.material.Load(); m != nil {
		if m.target != nil {
			return m.target.close(ctx)
		}
		return m.prepared.Close(ctx)
	}
	return nil // registration alone acquires no child/token resource
}
func (a *runtimeGuestActor) Acquire(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
	if a == nil || a.self != a || a.service == nil || !authBudget(ctx) || a.revoked.Load() {
		return nil, ErrProvisioningRejected
	}
	s := a.service
	s.mu.Lock()
	r, c, m := a.record, ticket.record, a.material.Load()
	valid := r != nil && r.confirmation == c && r.matchesLocked(a.owner) && !r.closed && r.prepareReplyReturned && c.liveLocked(time.Now()) && a.source.target.inspected.Load() && !a.source.target.revoked.Load() && m != nil && m.prepared != nil && m.target != nil && r.sid != ""
	if !valid {
		s.mu.Unlock()
		return nil, ErrProvisioningRejected
	}
	sid, descriptor := r.sid, cloneGuestDescriptor(r.descriptor).Preview
	s.mu.Unlock()
	if !m.target.matchesGuestSID(sid) || a.revoked.Load() || !authBudget(ctx) {
		return nil, ErrProvisioningRejected
	}
	o := a.source.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.revoked || o.expectedSID != sid || len(o.proofs) != 0 || a.revoked.Load() || !authBudget(ctx) {
		return nil, ErrProvisioningRejected
	}
	p := &confirmationRuntimeProof{source: a.source, owner: o, confirmation: c, service: s, issuer: o.issuer, auth: o.auth, outer: o.outer, descriptor: descriptor, stagingSID: sid, runtimeSID: sid, target: a.source.target, boundary: supervisor.SeparatedNonAdminGuest}
	p.self = p
	o.proofs[p] = struct{}{}
	return p, nil
}
func (a *runtimeGuestActor) CloseOwned(ctx context.Context, owner *confirmationEvidenceOwner) error {
	if !authBudget(ctx) || a.self != a || a.source == nil || owner != a.source.owner || !a.revoked.Load() {
		return ErrRuntimeUnconfirmed
	}
	s := a.service
	s.mu.Lock()
	r := a.record
	if r == nil {
		s.mu.Unlock()
		return nil // registration alone acquired no guest resource
	}
	valid := r.matchesLocked(a.owner) && s.stopping && r.confirmation.finished && s.reservation.finished
	closed := r.closed
	s.mu.Unlock()
	if !valid {
		return ErrRuntimeUnconfirmed
	}
	if closed {
		return nil
	}
	err := authCleanupCall(func() error { return a.owner.CloseRuntimeGuest(ctx, RuntimeGuestTicket{record: r}, r.lease) })
	if err != nil {
		return err
	}
	s.mu.Lock()
	r.closed = true // only the outer owner's real close reply is retained here
	s.mu.Unlock()
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	return nil
}
