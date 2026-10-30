package guestaccount

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// NeverCreatedStaging owns only disabled-account preparation. It supplies no
// runtime proof, authority, token ownership or executable session.
// The original execution thread retains partial constructor references too.
type NeverCreatedStaging struct {
	self      *NeverCreatedStaging
	ticket    servicehost.GuestStagingTicket
	owner     servicehost.GuestStagingOwner
	namespace servicehost.GuestStagingNamespace
	lease     *serviceready.InspectionLease
	staging   *stagingOwner
	closed    bool
}

// NewNeverCreatedStaging connects the held layout and actual Service ticket to
// the native stager. launch borrows the original owner's concrete LaunchToken;
// this function never owns Token.Close.
func NewNeverCreatedStaging(ctx context.Context, layout *FixedLayout, prepared *bootstrapauth.PreparedService, ready serviceready.Config, launch stagingLaunch, ticket servicehost.GuestStagingTicket, owner servicehost.GuestStagingOwner, lease *serviceready.InspectionLease) (held *NeverCreatedStaging, result error) {
	if layout == nil || !layout.configured || layout.runtime || owner == nil || !finite(ctx) {
		return nil, ErrUnconfigured
	}
	descriptor, err := ticket.DescriptorFor(ctx, owner)
	if err != nil {
		return nil, err
	}
	n := &NeverCreatedStaging{ticket: ticket, owner: owner, namespace: descriptor.Namespace, lease: lease}
	n.self = n
	held = n // ownership precedes Borrow/native acquisition and survives every error
	defer func() {
		if recover() != nil {
			result = bootstrapauth.ErrFatal
		}
	}()
	p, err := NewPlan(ProtectedInputs{sessionEpoch: descriptor.Namespace.Epoch, intentID: descriptor.Namespace.Intent, generation: descriptor.Namespace.Generation, machineSID: layout.machine, operatorSID: layout.operator, serviceUserSID: layout.user, policy: layout.policy})
	if err != nil {
		return n, err
	}
	if prepared == nil || launch == nil || lease == nil || !layout.MatchesService(ready.Profile.ServiceName, ready.Profile.Keys.ProcessUserSID, ready.Profile.Keys.ServiceSID, ready.Profile.Keys.OperatorSID) {
		return n, ErrUnconfigured
	}
	token, err := launch.Borrow(ctx)
	if err != nil || token == 0 {
		return n, errors.Join(ErrDenied, err)
	}
	profile := layout.profile
	profile.native.token = token
	profile.native.stdio = childstdio.Setup{UserSID: layout.user, ServiceSID: profile.serviceSID, CleanupTimeout: ready.CleanupTimeout}
	ready.PrimaryToken = token
	sessionSeal := &seal{10}
	session := newNeverCreatedSession(p, descriptor.Namespace, ticket, owner, sessionSeal)
	n.staging, err = newNativeStaging(ctx, p, stagingNativeConfig{profile: profile, prepared: prepared, ready: ready, launch: launch, control: lease}, session, sessionSeal)
	return n, err
}
func (n *NeverCreatedStaging) Prepare(ctx context.Context) error {
	if n == nil || n.self != n || n.closed || n.staging == nil || !finite(ctx) {
		return ErrUnconfigured
	}
	return n.staging.Prepare(ctx)
}
func (n *NeverCreatedStaging) Close(ctx context.Context) error {
	if n == nil || n.self != n || !finite(ctx) {
		return ErrCleanup
	}
	if n.closed {
		return nil
	}
	if n.staging != nil {
		if err := n.staging.Close(ctx); err != nil {
			return err
		}
	} else if err := n.ticket.RevokeNeverCreated(ctx, n.owner, n.namespace, ""); err != nil {
		return err
	}
	// A constructor can fail before installing the scope's control reference.
	// Close the same lease only after the actual guest owner has joined.
	if n.lease != nil {
		if err := n.lease.Close(ctx); err != nil {
			return err
		}
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	n.closed = true
	return nil
}
