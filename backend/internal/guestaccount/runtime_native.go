package guestaccount

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// NewRuntimeGuest is the explicit native Batch candidate. Only a runtime layout,
// the registered actual Service ticket and original execution owner reach it.
// It accepts no account identity, password or primary-token handle from callers.
// The caller retains every partial owner until Close acknowledges actual cleanup.
func NewRuntimeGuest(ctx context.Context, layout *FixedLayout, prepared *bootstrapauth.PreparedService, ready serviceready.Config, launch stagingLaunch, ticket servicehost.RuntimeGuestTicket, owner servicehost.RuntimeGuestOwner, lease *serviceready.InspectionLease) (held *RuntimeGuest, result error) {
	if layout == nil || !layout.configured || !layout.runtime || !layout.policy.batch || owner == nil || !finite(ctx) {
		return nil, ErrUnconfigured
	}
	probe, err := runtimeProbeSpec(layout, ready.CleanupTimeout)
	if err != nil {
		return nil, err
	}
	descriptor, err := ticket.DescriptorFor(ctx, owner)
	if err != nil {
		return nil, err
	}
	p, err := NewPlan(ProtectedInputs{sessionEpoch: descriptor.Namespace.Epoch, intentID: descriptor.Namespace.Intent, generation: descriptor.Namespace.Generation, machineSID: layout.machine, operatorSID: layout.operator, serviceUserSID: layout.user, policy: layout.policy})
	if err != nil {
		return nil, err
	}
	r := &RuntimeGuest{plan: p, probe: probe, lease: lease, sessionSeal: &seal{11}, authority: &runtimeTicketAuthority{ticket: ticket, owner: owner, namespace: descriptor.Namespace}}
	r.self = r
	held = r // published before Borrow or any native acquisition
	defer func() {
		if recover() != nil {
			r.fatal = true
			result = bootstrapauth.ErrFatal
		}
	}()
	r.ops, err = runtimePlatform(r)
	if err != nil {
		return r, err
	}
	if prepared == nil || launch == nil || lease == nil || !layout.MatchesService(ready.Profile.ServiceName, ready.Profile.Keys.ProcessUserSID, ready.Profile.Keys.ServiceSID, ready.Profile.Keys.OperatorSID) {
		return r, ErrUnconfigured
	}
	token, err := launch.Borrow(ctx)
	if err != nil || !realJournalHandle(token) || !finite(ctx) {
		return r, ErrDenied
	}
	profile := layout.profile
	profile.native.token = token
	profile.native.stdio = childstdio.Setup{UserSID: layout.user, ServiceSID: profile.serviceSID, CleanupTimeout: ready.CleanupTimeout}
	ready.PrimaryToken = token
	stage, err := newNativeStaging(ctx, p, stagingNativeConfig{profile: profile, prepared: prepared, ready: ready, launch: launch, control: lease}, r, r.sessionSeal)
	if stage != nil {
		r.stage, r.lifecycle = stage, stage.lifecycle
	}
	return r, err
}

type runtimeTicketAuthority struct {
	ticket    servicehost.RuntimeGuestTicket
	owner     servicehost.RuntimeGuestOwner
	namespace servicehost.GuestStagingNamespace
}

func (a *runtimeTicketAuthority) bind(ctx context.Context, b binding) error {
	return a.ticket.BindCreatedGuest(ctx, a.owner, a.namespace, b.sid)
}
func (a *runtimeTicketAuthority) revoke(ctx context.Context, b binding) error {
	return a.ticket.RevokeRuntimeGuest(ctx, a.owner, a.namespace, b.sid)
}

// nativeScope checks the original owner thread independently of admission.
func (r *RuntimeGuest) nativeScope() bool {
	s, ok := r.stage.(*stagingOwner)
	if !ok || s == nil || s.blocked || s.fatal {
		return false
	}
	if s.accounts != nil && !s.accountsClosing && stagingQueryUnsafe(s.accounts) {
		s.blocked = true
		return false
	}
	return s.rawScope()
}
