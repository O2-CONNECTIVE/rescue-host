package servicehost

import (
	"context"
	"slices"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// RuntimeGuestOwner is the already bound outer owner. Its preparation reply is
// retained material, not a SID assertion, authentication, or separation proof.
type RuntimeGuestOwner interface {
	PrepareRuntimeGuest(context.Context, RuntimeGuestTicket, *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error)
	CloseRuntimeGuest(context.Context, RuntimeGuestTicket, *serviceready.InspectionLease) error
}

// RuntimeGuestTicket identifies one runtime preparation by its original owner.
// It cannot be constructed from a descriptor or used as a physical cleanup proof.
type RuntimeGuestTicket struct{ record *runtimeGuestRecord }

type runtimeGuestRecord struct {
	self                                             *runtimeGuestRecord
	actor                                            *runtimeGuestActor
	confirmation                                     *previewConfirmation
	descriptor                                       GuestStagingDescriptor
	lease                                            *serviceready.InspectionLease
	sid                                              string
	prepareReplyReturned, inspectionReturned, closed bool // Service.mu; RPC return is not native worker completion
}

func (r *runtimeGuestRecord) matchesLocked(owner RuntimeGuestOwner) bool {
	if r == nil || r.self != r || r.actor == nil {
		return false
	}
	a, s := r.actor, r.actor.service
	if a.self != a || s == nil || s.preview == nil || s.preview.runtimeGuest != a || a.record != r || !sameGuestOwner(a.owner, owner) || !sameGuestOwner(s.outer, owner) || s.guestOwner != nil || s.executionClosed {
		return false
	}
	source, registration := a.source, s.preview.targetOwner
	if source == nil || source.self != source || source != s.preview.runtime || source.kernel != a || registration == nil || registration.source != source || registration.kernel != a || registration.owner != source.owner || registration.target != source.target {
		return false
	}
	o, c := source.owner, r.confirmation
	return o != nil && o.self == o && o.source == source && o.target == registration.target && o.service == s && o.issuer == s.issuer && o.auth == s.authOwner && o.outer == s.outerTicket && c != nil && c.intent != nil && c.service == s && c.issuer == s.issuer && c.owner == s.authOwner && c.runtimeSource == source && c.runtimeOwner == o && c.runtimeTarget == registration.target && s.preview.intent == c.intent && c.intent.confirmation == c && s.reservation == c.work.reservation && s.reservation != nil && s.reservation.claimed
}

func (t RuntimeGuestTicket) MatchesService(s *Service) bool {
	return s != nil && t.record != nil && t.record.actor != nil && t.record.actor.service == s
}

func (t RuntimeGuestTicket) DescriptorFor(ctx context.Context, owner RuntimeGuestOwner) (GuestStagingDescriptor, error) {
	r := t.record
	if r == nil || r.actor == nil || r.actor.service == nil || !authBudget(ctx) || !r.actor.service.mu.TryLock() {
		return GuestStagingDescriptor{}, ErrProvisioningRejected
	}
	defer r.actor.service.mu.Unlock()
	if !r.matchesLocked(owner) || r.closed || !r.confirmation.liveLocked(time.Now()) || !authBudget(ctx) {
		return GuestStagingDescriptor{}, ErrProvisioningRejected
	}
	return cloneGuestDescriptor(r.descriptor), nil
}

// BindCreatedGuest records the original owner's actual creation receipt only.
// Cancellation, RPC return and confirmation completion do not erase a late SID:
// that owner may still be joining native work. No acquisition or grant is issued.
func (t RuntimeGuestTicket) BindCreatedGuest(_ context.Context, owner RuntimeGuestOwner, ns GuestStagingNamespace, sid string) error {
	r := t.record
	if r == nil || r.actor == nil || r.actor.service == nil {
		return ErrProvisioningRejected
	}
	s := r.actor.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if !r.matchesLocked(owner) || r.closed || r.descriptor.Namespace != ns || !validRuntimeGuestSID(sid) || slices.Contains(s.preview.excludedSIDs, sid) {
		return ErrProvisioningRejected
	}
	o := r.actor.source.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	if r.sid != "" {
		if r.sid == sid && o.expectedSID == sid {
			return nil
		}
		return ErrProvisioningRejected
	}
	if o.expectedSID != "" {
		return ErrProvisioningRejected
	}
	r.sid, o.expectedSID = sid, sid
	return nil
}

// RevokeRuntimeGuest closes only this registration's canonical issuer/auth
// owner. It neither observes nor certifies physical guest/child/token cleanup.
func (t RuntimeGuestTicket) RevokeRuntimeGuest(ctx context.Context, owner RuntimeGuestOwner, ns GuestStagingNamespace, sid string) error {
	r := t.record
	if r == nil || r.actor == nil || r.actor.service == nil {
		return ErrRuntimeUnconfirmed
	}
	s := r.actor.service
	s.mu.Lock()
	valid := r.matchesLocked(owner) && r.descriptor.Namespace == ns && r.sid == sid
	if !valid {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	o := r.actor.source.owner
	s.mu.Unlock()
	if !valid || o.revoke() != nil || !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

// BindRuntimeGuestOwner fixes the outer actor before Service use. The actor is
// registered with the actual issuer/auth owner inside Serve, before acquisition.
func (s *Service) BindRuntimeGuestOwner(owner RuntimeGuestOwner) error {
	if s == nil || owner == nil {
		return ErrProvisioningUnconfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil || s.preview == nil || s.control == nil || s.dispatch == nil || s.dispatch.service != s || s.auth == nil || s.auth.service != s || s.cfg.Launcher != nil || s.cfg.Provisioning != nil || !sameGuestOwner(s.outer, owner) {
		return ErrProvisioningRejected
	}
	if s.used || s.started || s.stopping || s.guestOwner != nil || s.executionClosed || s.preview.runtimeGuest != nil || s.preview.runtime != nil || s.preview.targetOwner != nil {
		return ErrAlreadyUsed
	}
	a := &runtimeGuestActor{service: s, owner: owner, kernel: runtimeGuestNativeKernel{}}
	a.self = a
	s.preview.runtimeGuest = a
	return nil
}
