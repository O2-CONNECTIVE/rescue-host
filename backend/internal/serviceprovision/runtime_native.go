package serviceprovision

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// Private seam around the concrete native guest owner. No caller can provide a
// SID, token, arbitrary executable or successful inspection callback here.
type nativeRuntimeGuest interface {
	Prepare(context.Context) (*childjob.Prepared, error)
	Expectation(context.Context) (childjob.GuestTokenExpectation, error)
	Close(context.Context) error
}
type nativeRuntimeAttempt struct {
	ticket        servicehost.RuntimeGuestTicket
	owner         servicehost.RuntimeGuestOwner
	lease         *serviceready.InspectionLease
	held          nativeRuntimeGuest
	closed, fatal bool
}

func (s *nativeExecutionStage) PrepareRuntimeGuest(ctx context.Context, t servicehost.RuntimeGuestTicket, owner servicehost.RuntimeGuestOwner, lease *serviceready.InspectionLease) (p *childjob.Prepared, expectation childjob.GuestTokenExpectation, result error) {
	if !s.runtimeGuest || s.staging || s.guest == nil || !s.guest.runtimeGuest || s.prepared == nil || s.runtimeAttempt != nil || !t.MatchesService(s.service) {
		return nil, expectation, ErrRootDenied
	}
	d, err := t.DescriptorFor(ctx, owner)
	if err != nil {
		return nil, expectation, err
	}
	a := &nativeRuntimeAttempt{ticket: t, owner: owner, lease: lease}
	s.runtimeAttempt = a // retained before launch-token or native guest acquisition
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
	if err = s.VerifyPreviewConfirmation(ctx, d.Preview); err != nil {
		return nil, expectation, err
	}
	launch := s.calls.launch
	if launch == nil {
		launch = prepareExecutionToken
	}
	s.token, err = launch(ctx, s.prepared)
	if err != nil || s.token == nil {
		return nil, expectation, errors.Join(ErrRootDenied, err)
	}
	build := s.calls.runtime
	if build == nil {
		build = buildNativeRuntimeGuest
	}
	a.held, err = build(ctx, s, t, owner, lease)
	if err != nil || a.held == nil || !rootLive(ctx) {
		return nil, expectation, errors.Join(ErrRootDenied, err)
	}
	p, err = a.held.Prepare(ctx) // owner retains partial Prepared before this reply
	if err != nil || p == nil || !rootLive(ctx) {
		return p, expectation, errors.Join(ErrRootDenied, err)
	}
	expectation, err = a.held.Expectation(ctx)
	if err != nil || !rootLive(ctx) {
		return p, expectation, errors.Join(ErrRootDenied, err)
	}
	return p, expectation, nil
}

func buildNativeRuntimeGuest(ctx context.Context, s *nativeExecutionStage, t servicehost.RuntimeGuestTicket, owner servicehost.RuntimeGuestOwner, lease *serviceready.InspectionLease) (nativeRuntimeGuest, error) {
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
	held, err := guestaccount.NewRuntimeGuest(ctx, layout, prepared, ready, s.token, t, owner, lease)
	if held == nil {
		return nil, err
	}
	return held, err
}

func (s *nativeExecutionStage) CloseRuntimeGuest(ctx context.Context, t servicehost.RuntimeGuestTicket, owner servicehost.RuntimeGuestOwner, lease *serviceready.InspectionLease) error {
	if !s.runtimeGuest || s.staging || !rootLive(ctx) || !t.MatchesService(s.service) {
		return ErrRootUnconfirmed
	}
	a := s.runtimeAttempt
	if a == nil {
		a = &nativeRuntimeAttempt{ticket: t, owner: owner, lease: lease}
		s.runtimeAttempt = a // failed/unstarted attempt is still consumed
	}
	if a.ticket != t || a.owner != owner || a.lease != lease || a.fatal {
		return ErrRootUnconfirmed
	}
	if a.closed {
		return nil
	}
	if a.held != nil {
		if err := a.held.Close(ctx); err != nil {
			return err
		}
	}
	if !rootLive(ctx) {
		return ErrRootUnconfirmed
	}
	a.closed = true // actual same-owner guest/worker cleanup only
	return nil
}
