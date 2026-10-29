package serviceprovision

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

var _ servicehost.GuestStagingOwner = (*Execution)(nil)

type nativeGuestStager interface {
	Prepare(context.Context) error
	Close(context.Context) error
}
type nativeGuestAttempt struct {
	ticket        servicehost.GuestStagingTicket
	owner         servicehost.GuestStagingOwner
	lease         *serviceready.InspectionLease
	held          nativeGuestStager
	closed, fatal bool
}

func (e *Execution) PrepareNeverCreatedGuest(ctx context.Context, t servicehost.GuestStagingTicket, l *serviceready.InspectionLease) error {
	if e == nil || !e.configured || !e.staging || !rootLive(ctx) {
		return ErrRootDenied
	}
	if _, err := t.DescriptorFor(ctx, e); err != nil {
		return ErrRootDenied
	}
	return e.guestRPC(ctx, t, l, false)
}
func (e *Execution) CloseNeverCreatedGuest(ctx context.Context, t servicehost.GuestStagingTicket, l *serviceready.InspectionLease) error {
	if e == nil || !e.configured || !e.staging || !rootLive(ctx) {
		return ErrRootUnconfirmed
	}
	if _, err := t.CleanupDescriptorFor(ctx, e); err != nil {
		return ErrRootUnconfirmed
	}
	return e.guestRPC(ctx, t, l, true)
}
func (e *Execution) guestRPC(ctx context.Context, t servicehost.GuestStagingTicket, l *serviceready.InspectionLease, closeGuest bool) error {
	request := executionRequest{ctx: ctx, guest: &t, lease: l, closeGuest: closeGuest, reply: make(chan error, 1)}
	select {
	case <-e.cleaned:
		return ErrRootUnconfirmed
	case <-ctx.Done():
		return ErrRootUnconfirmed
	case e.requests <- request:
	}
	// Cancellation does not release any transferred reference. The original queue
	// retains the in-flight SDK/partial result, then processes the cleanup RPC.
	select {
	case <-ctx.Done():
		return ErrRootUnconfirmed
	case <-e.cleaned:
		return ErrRootUnconfirmed
	case err := <-request.reply:
		return err
	}
}

type guestExecutionStage interface {
	PrepareGuest(context.Context, servicehost.GuestStagingTicket, servicehost.GuestStagingOwner, *serviceready.InspectionLease) error
	CloseGuest(context.Context, servicehost.GuestStagingTicket, servicehost.GuestStagingOwner, *serviceready.InspectionLease) error
}

func (e *Execution) guestOnOwner(ctx context.Context, t servicehost.GuestStagingTicket, l *serviceready.InspectionLease, closeGuest bool) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRootFatal
		}
	}()
	stage, ok := e.stage.(guestExecutionStage)
	if !ok || !e.staging {
		return ErrRootDenied
	}
	if closeGuest {
		return stage.CloseGuest(ctx, t, e, l)
	}
	return stage.PrepareGuest(ctx, t, e, l)
}
func (s *nativeExecutionStage) PrepareGuest(ctx context.Context, t servicehost.GuestStagingTicket, owner servicehost.GuestStagingOwner, l *serviceready.InspectionLease) (result error) {
	if !s.staging || s.guest == nil || s.prepared == nil || s.guestAttempt != nil {
		return ErrRootDenied
	}
	descriptor, err := t.DescriptorFor(ctx, owner)
	if err != nil {
		return err
	}
	a := &nativeGuestAttempt{ticket: t, owner: owner, lease: l}
	s.guestAttempt = a
	defer func() {
		if recover() != nil {
			a.fatal = true
			result = ErrRootFatal
		}
		if errors.Is(result, bootstrapauth.ErrFatal) || errors.Is(result, serviceready.ErrFatal) {
			a.fatal = true
			result = errors.Join(ErrRootFatal, result)
		}
	}()
	if err = s.VerifyPreviewConfirmation(ctx, descriptor.Preview); err != nil {
		return err
	}
	launch := s.calls.launch
	if launch == nil {
		launch = prepareExecutionToken
	}
	s.token, err = launch(ctx, s.prepared) // partial concrete token retained before error
	if err != nil || s.token == nil {
		return errors.Join(ErrRootDenied, err)
	}
	build := s.calls.staging
	if build == nil {
		build = buildNativeGuestStaging
	}
	a.held, err = build(ctx, s, t, owner, l) // partial GA owner precedes ctx/error inspection
	if err != nil || a.held == nil {
		return errors.Join(ErrRootDenied, err)
	}
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	return a.held.Prepare(ctx)
}
func buildNativeGuestStaging(ctx context.Context, s *nativeExecutionStage, t servicehost.GuestStagingTicket, owner servicehost.GuestStagingOwner, l *serviceready.InspectionLease) (nativeGuestStager, error) {
	layout, err := s.guest.Layout(ctx)
	if err != nil {
		return nil, err
	}
	prepared, ok := s.prepared.scope.(*bootstrapauth.PreparedService)
	if !ok {
		return nil, ErrRootDenied
	}
	r := s.prepared.record
	ready := serviceready.Config{Profile: r.Profile, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout}
	held, err := guestaccount.NewNeverCreatedStaging(ctx, layout, prepared, ready, s.token, t, owner, l)
	if held == nil {
		return nil, err
	}
	return held, err
}
func (s *nativeExecutionStage) CloseGuest(ctx context.Context, t servicehost.GuestStagingTicket, owner servicehost.GuestStagingOwner, l *serviceready.InspectionLease) error {
	if !s.staging || !rootLive(ctx) {
		return ErrRootUnconfirmed
	}
	descriptor, err := t.CleanupDescriptorFor(ctx, owner)
	if err != nil {
		return err
	}
	a := s.guestAttempt
	if a == nil {
		a = &nativeGuestAttempt{ticket: t, owner: owner, lease: l}
		s.guestAttempt = a
	}
	if a.ticket != t || a.owner != owner || a.lease != l || a.fatal {
		return ErrRootUnconfirmed
	}
	if a.closed {
		return nil
	}
	if a.held != nil {
		if err = a.held.Close(ctx); err != nil {
			return err
		}
	} else {
		// No native stager was acquired. Confirm the same permanent no-exec record,
		// then close this exact lease. This is not an empty-session runtime proof.
		if err = t.RevokeNeverCreated(ctx, owner, descriptor.Namespace, ""); err != nil {
			return err
		}
		if l != nil {
			if err = l.Close(ctx); err != nil {
				return err
			}
		}
	}
	if !rootLive(ctx) {
		return ErrRootUnconfirmed
	}
	a.closed = true
	return nil
}
