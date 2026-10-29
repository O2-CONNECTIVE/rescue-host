package servicehost

import (
	"context"
	"reflect"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// GuestStagingOwner operates only on the already bound outer owner's thread.
// Preparing a disabled guest is neither runtime separation nor an execution grant.
type GuestStagingOwner interface {
	PrepareNeverCreatedGuest(context.Context, GuestStagingTicket, *serviceready.InspectionLease) error
	CloseNeverCreatedGuest(context.Context, GuestStagingTicket, *serviceready.InspectionLease) error
}
type GuestStagingNamespace struct {
	Epoch, Intent [32]byte
	Generation    uint64
}
type GuestStagingDescriptor struct {
	Preview   PreviewDescriptor
	Namespace GuestStagingNamespace
}
type GuestStagingTicket struct{ record *guestStagingRecord }
type guestStagingRecord struct {
	service             *Service
	owner               GuestStagingOwner
	confirmation        *previewConfirmation
	descriptor          GuestStagingDescriptor
	lease               *serviceready.InspectionLease
	sid                 string
	transferred, closed bool
	prepareJoined       bool // successful reply from the original owner after SDK actual return
}

func sameGuestOwner(a, b any) bool {
	return a != nil && b != nil && reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.ValueOf(a).Kind() == reflect.Pointer && !reflect.ValueOf(a).IsNil() && !reflect.ValueOf(b).IsNil() && a == b
}
func (s *Service) BindGuestStagingOwner(owner GuestStagingOwner) error {
	if s == nil || owner == nil {
		return ErrProvisioningUnconfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil || s.preview == nil || s.control == nil || s.dispatch == nil || s.dispatch.service != s || s.auth == nil || s.auth.service != s || s.cfg.Launcher != nil || s.cfg.Provisioning != nil || !sameGuestOwner(s.outer, owner) {
		return ErrProvisioningRejected
	}
	if s.used || s.started || s.stopping || s.guestOwner != nil {
		return ErrAlreadyUsed
	}
	s.guestOwner = owner
	s.executionClosed = true // irreversible before any Service acquisition/admission
	return nil
}
func (r *guestStagingRecord) matchesLocked(owner GuestStagingOwner) bool {
	s := r.service
	return sameGuestOwner(r.owner, owner) && sameGuestOwner(s.outer, owner) && sameGuestOwner(s.guestOwner, owner) && s.staging == r && s.executionClosed && r.confirmation != nil && r.confirmation.service == s && r.confirmation.issuer == s.issuer && r.confirmation.owner == s.authOwner && s.preview != nil && s.preview.intent == r.confirmation.intent && s.preview.intent.confirmation == r.confirmation && s.reservation == r.confirmation.work.reservation && s.reservation != nil && s.reservation.claimed && s.reservation.purpose == reservationGuestPreparation
}

// MatchesService observes immutable provenance only. It grants no acquisition,
// cleanup, authentication or execution permission; callers also use the gated
// descriptor/owner/deadline methods.
func (t GuestStagingTicket) MatchesService(s *Service) bool {
	return s != nil && t.record != nil && t.record.service == s
}

func (t GuestStagingTicket) DescriptorFor(ctx context.Context, owner GuestStagingOwner) (GuestStagingDescriptor, error) {
	r := t.record
	if r == nil || !authBudget(ctx) || !r.service.mu.TryLock() {
		return GuestStagingDescriptor{}, ErrProvisioningRejected
	}
	defer r.service.mu.Unlock()
	if !r.matchesLocked(owner) || r.closed || !r.confirmation.liveLocked(time.Now()) {
		return GuestStagingDescriptor{}, ErrProvisioningRejected
	}
	return cloneGuestDescriptor(r.descriptor), nil
}
func (t GuestStagingTicket) CleanupDescriptorFor(ctx context.Context, owner GuestStagingOwner) (GuestStagingDescriptor, error) {
	r := t.record
	if r == nil || !authBudget(ctx) || !r.service.mu.TryLock() {
		return GuestStagingDescriptor{}, ErrRuntimeUnconfirmed
	}
	defer r.service.mu.Unlock()
	if !r.matchesLocked(owner) || !r.service.stopping || !r.confirmation.finished || !r.service.reservation.finished {
		return GuestStagingDescriptor{}, ErrRuntimeUnconfirmed
	}
	return cloneGuestDescriptor(r.descriptor), nil
}
func cloneGuestDescriptor(d GuestStagingDescriptor) GuestStagingDescriptor {
	d.Preview.AllowedKeyFingerprints = append([]string(nil), d.Preview.AllowedKeyFingerprints...)
	return d
}
func (t GuestStagingTicket) cleanupTargetLocked(owner GuestStagingOwner, ns GuestStagingNamespace, sid string) bool {
	r := t.record
	if !r.matchesLocked(owner) || r.descriptor.Namespace != ns || !r.service.stopping || !r.confirmation.finished || !r.service.reservation.finished {
		return false
	}
	if sid != "" {
		if !validRuntimeGuestSID(sid) {
			return false
		}
		for _, excluded := range r.service.preview.excludedSIDs {
			if sid == excluded {
				return false
			}
		}
		if r.sid == "" {
			r.sid = sid
		} else if r.sid != sid {
			return false
		}
	}
	return r.sid == sid
}
func (t GuestStagingTicket) RevokeNeverCreated(ctx context.Context, owner GuestStagingOwner, ns GuestStagingNamespace, sid string) error {
	r := t.record
	if r == nil || !authBudget(ctx) || !r.service.mu.TryLock() {
		return ErrRuntimeUnconfirmed
	}
	s := r.service
	valid := t.cleanupTargetLocked(owner, ns, sid)
	canonical := s.authOwner
	s.mu.Unlock()
	if !valid || canonical == nil {
		return ErrRuntimeUnconfirmed
	}
	if err := canonical.Revoke(); err != nil {
		return ErrRuntimeUnconfirmed
	}
	return t.ConfirmNeverCreated(ctx, owner, ns, sid)
}
func (t GuestStagingTicket) ConfirmNeverCreated(ctx context.Context, owner GuestStagingOwner, ns GuestStagingNamespace, sid string) error {
	r := t.record
	if r == nil || !authBudget(ctx) || !r.service.mu.TryLock() {
		return ErrRuntimeUnconfirmed
	}
	s := r.service
	defer s.mu.Unlock()
	if !t.cleanupTargetLocked(owner, ns, sid) || s.host != nil || s.delivery != nil || s.executionAdmissions != 0 {
		return ErrRuntimeUnconfirmed
	}
	actual, ok := s.issuer.TrySnapshot()
	if !ok || !actual.Closed || actual.Generation != 0 || actual.Consumed || actual.HasAuthority || !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	return nil
}
