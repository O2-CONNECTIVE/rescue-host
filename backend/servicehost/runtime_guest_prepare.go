package servicehost

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

func (a *runtimeGuestActor) PrepareTarget(ctx context.Context, ticket PreviewConfirmationTicket) error {
	if a == nil || a.self != a || a.service == nil || a.kernel == nil || !authBudget(ctx) || !authBudget(a.admission) {
		return ErrProvisioningRejected
	}
	var ns GuestStagingNamespace
	if _, err := rand.Read(ns.Epoch[:]); err != nil {
		return ErrProvisioningRejected
	}
	if _, err := rand.Read(ns.Intent[:]); err != nil {
		return ErrProvisioningRejected
	}
	ns.Generation = 1 // fresh guest namespace, independent of the canonical issuer generation
	if ns.Epoch == ([32]byte{}) || ns.Intent == ([32]byte{}) {
		return ErrProvisioningRejected
	}
	s, c := a.service, ticket.record
	s.mu.Lock()
	if !a.matchesRegistrationLocked() || c == nil || c.service != s || a.record != nil || c.runtimeSource != a.source || c.runtimeOwner != a.source.owner || c.runtimeTarget != a.source.target || !c.liveLocked(time.Now()) || a.revoked.Load() {
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	lease := a.kernel.inspection(c.request)
	if lease == nil {
		s.mu.Unlock()
		return ErrProvisioningUnconfigured
	}
	r := &runtimeGuestRecord{actor: a, confirmation: c, descriptor: cloneGuestDescriptor(GuestStagingDescriptor{Preview: s.preview.descriptor, Namespace: ns}), lease: lease}
	r.self, a.record = r, r // ownership precedes the outer owner's first callback
	r.requestWatch = newRuntimeGuestWatch(ctx, func() { s.stopFor(ErrRuntimeUnconfirmed) })
	s.mu.Unlock()
	defer r.requestWatch.stopWatching()
	p, expectation, prepareErr := callRuntimeGuestPrepare(a.admission, a, RuntimeGuestTicket{record: r})
	requestDetached := r.requestWatch.stopWatching()
	var materialErr error
	if p != nil {
		m, err := runtimeGuestMaterialFor(a.kernel, p, expectation)
		materialErr = err
		a.material.Store(m) // preserve every partial Prepared before cancellation/error checks
	}
	s.mu.Lock()
	r.prepareReplyReturned = true
	// A successful owner's material reply acknowledges its short inspection borrow.
	// An error/canceled RPC may precede native return and keeps the borrow retained.
	r.inspectionReturned = prepareErr == nil && p != nil
	live := r.matchesLocked(a.owner) && !r.closed && r.sid != "" && c.liveLocked(time.Now())
	s.mu.Unlock()
	if prepareErr != nil || materialErr != nil || p == nil || !live || a.revoked.Load() || !authBudget(ctx) || !authBudget(a.admission) || !requestDetached {
		_ = a.revoke() // same target's memory latch; outer Close still owns actual worker cleanup
		if prepareErr != nil {
			return prepareErr
		}
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func callRuntimeGuestPrepare(ctx context.Context, a *runtimeGuestActor, ticket RuntimeGuestTicket) (p *childjob.Prepared, e childjob.GuestTokenExpectation, err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return a.owner.PrepareRuntimeGuest(ctx, ticket, ticket.record.lease)
}
