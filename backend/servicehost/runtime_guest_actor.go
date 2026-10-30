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
	self            *runtimeGuestActor
	service         *Service
	owner           RuntimeGuestOwner
	kernel          runtimeGuestKernel
	admission       context.Context
	cancelAdmission context.CancelFunc
	admissionWatch  *runtimeGuestWatch
	source          *confirmationRuntimeSource
	evidence        *confirmationEvidenceOwner
	target          *confirmationTarget
	record          *runtimeGuestRecord // Service.mu
	material        atomic.Pointer[runtimeGuestMaterial]
	revoked         atomic.Bool
}

func (s *Service) bindRuntimeGuestLocked(parent context.Context) error {
	if s.preview == nil || s.preview.runtimeGuest == nil {
		return nil
	}
	a := s.preview.runtimeGuest
	if a.self != a || a.service != s || !sameGuestOwner(s.outer, a.owner) || parent == nil || parent.Err() != nil || s.issuer == nil || s.authOwner == nil || s.guestOwner != nil || s.executionClosed || s.preview.runtime != nil || s.preview.targetOwner != nil || s.stopping {
		return ErrProvisioningRejected
	}
	target := &confirmationTarget{ops: a}
	source := &confirmationRuntimeSource{kernel: a, target: target}
	source.self = source
	o := &confirmationEvidenceOwner{source: source, service: s, issuer: s.issuer, auth: s.authOwner, outer: s.outerTicket, target: target, proofs: make(map[*confirmationRuntimeProof]struct{})}
	o.self, source.owner = o, o
	a.source = source
	a.evidence, a.target = o, target
	a.admission, a.cancelAdmission = context.WithDeadline(parent, s.preview.descriptor.HardDeadline)
	a.admissionWatch = newRuntimeGuestWatch(a.admission, func() { s.stopFor(ErrRuntimeUnconfirmed) })
	s.preview.runtime = source
	s.preview.targetOwner = &confirmationTargetOwner{source: source, owner: o, kernel: a, target: target}
	return nil
}

func (a *runtimeGuestActor) matchesRegistrationLocked() bool {
	if !a.ownsRegistrationLocked() {
		return false
	}
	s, source := a.service, a.source
	return s.preview.runtimeGuest == a && s.guestOwner == nil && !s.executionClosed && source.self == source && source == s.preview.runtime && source.kernel == a && source.owner == a.evidence && source.target == a.target && a.target.ops == a
}

// Cleanup and late receipt retention use the original registration, independent
// of mutable source reports. Such reports can refuse authority, never retarget it.
func (a *runtimeGuestActor) ownsRegistrationLocked() bool {
	if a == nil || a.self != a || a.service == nil || a.source == nil || a.evidence == nil || a.target == nil {
		return false
	}
	s, o := a.service, a.evidence
	if s.preview == nil || !sameGuestOwner(s.outer, a.owner) {
		return false
	}
	registration := s.preview.targetOwner
	return registration != nil && registration.source == a.source && registration.kernel == a && registration.owner == o && registration.target == a.target && o.self == o && o.source == a.source && o.target == a.target && o.service == s && o.issuer == s.issuer && o.auth == s.authOwner && o.outer == s.outerTicket
}

func (a *runtimeGuestActor) inspect(ctx context.Context) error {
	m := a.material.Load()
	if m == nil || m.target == nil || a.revoked.Load() || !authBudget(ctx) || !authBudget(a.admission) {
		return ErrProvisioningRejected
	}
	return m.target.inspect(ctx)
}
func (a *runtimeGuestActor) commit(ctx context.Context) error {
	m := a.material.Load()
	if m == nil || m.target == nil || a.revoked.Load() || !authBudget(ctx) || !authBudget(a.admission) {
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
	if a == nil || a.self != a || a.service == nil || !authBudget(ctx) || !authBudget(a.admission) || a.revoked.Load() {
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
	if !m.target.matchesGuestSID(sid) || a.revoked.Load() || !authBudget(ctx) || !authBudget(a.admission) {
		return nil, ErrProvisioningRejected
	}
	o := a.evidence
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.revoked || o.expectedSID != sid || len(o.proofs) != 0 || a.revoked.Load() || !authBudget(ctx) || !authBudget(a.admission) {
		return nil, ErrProvisioningRejected
	}
	p := &confirmationRuntimeProof{source: a.source, owner: o, confirmation: c, service: s, issuer: o.issuer, auth: o.auth, outer: o.outer, descriptor: descriptor, stagingSID: sid, runtimeSID: sid, target: a.target, boundary: supervisor.SeparatedNonAdminGuest}
	p.self = p
	o.proofs[p] = struct{}{}
	return p, nil
}
func (a *runtimeGuestActor) CloseOwned(ctx context.Context, owner *confirmationEvidenceOwner) error {
	if !authBudget(ctx) || a.self != a || a.source == nil || owner != a.evidence || !a.revoked.Load() {
		return ErrRuntimeUnconfirmed
	}
	s := a.service
	s.mu.Lock()
	r := a.record
	if r == nil {
		s.mu.Unlock()
		return a.releaseAdmission(ctx) // registration owns its cancellation watchers only
	}
	valid := r.ownsLocked(a.owner) && s.stopping && r.confirmation.finished && s.reservation.finished
	closed := r.closed
	s.mu.Unlock()
	if !valid {
		return ErrRuntimeUnconfirmed
	}
	if closed {
		return a.releaseAdmission(ctx)
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
	return a.releaseAdmission(ctx)
}
