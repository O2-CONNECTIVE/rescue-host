package serviceprovision

import (
	"context"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

var _ servicehost.RuntimeGuestOwner = (*Execution)(nil)

// The stage owns all native partials before returning. Execution additionally
// retains every returned Prepared until this exact stage acknowledges Close.
type runtimeGuestStage interface {
	PrepareRuntimeGuest(context.Context, servicehost.RuntimeGuestTicket, servicehost.RuntimeGuestOwner, *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error)
	CloseRuntimeGuest(context.Context, servicehost.RuntimeGuestTicket, servicehost.RuntimeGuestOwner, *serviceready.InspectionLease) error
}

type runtimeGuestAttempt struct {
	ticket      servicehost.RuntimeGuestTicket
	lease       *serviceready.InspectionLease
	prepared    *childjob.Prepared
	expectation childjob.GuestTokenExpectation
	closed      bool
}

type runtimeGuestResult struct {
	prepared    *childjob.Prepared
	expectation childjob.GuestTokenExpectation
	err         error
}

func runtimeGuestLive(ctx context.Context) bool {
	if !rootLive(ctx) {
		return false
	}
	deadline, _ := ctx.Deadline()
	return time.Now().Before(deadline)
}

func (e *Execution) PrepareRuntimeGuest(ctx context.Context, ticket servicehost.RuntimeGuestTicket, lease *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
	if e == nil || !e.configured || !e.runtimeGuest || !runtimeGuestLive(ctx) || !ticket.MatchesService(e.service.Load()) {
		return nil, childjob.GuestTokenExpectation{}, ErrRootDenied
	}
	if _, err := ticket.DescriptorFor(ctx, e); err != nil {
		return nil, childjob.GuestTokenExpectation{}, ErrRootDenied
	}
	return e.runtimeGuestRPC(ctx, ticket, lease, false)
}

func (e *Execution) CloseRuntimeGuest(ctx context.Context, ticket servicehost.RuntimeGuestTicket, lease *serviceready.InspectionLease) error {
	if e == nil || !e.configured || !e.runtimeGuest || !runtimeGuestLive(ctx) || !ticket.MatchesService(e.service.Load()) {
		return ErrRootUnconfirmed
	}
	_, _, err := e.runtimeGuestRPC(ctx, ticket, lease, true)
	return err
}

func (e *Execution) runtimeGuestRPC(ctx context.Context, ticket servicehost.RuntimeGuestTicket, lease *serviceready.InspectionLease, closeGuest bool) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
	if !runtimeGuestLive(ctx) {
		return nil, childjob.GuestTokenExpectation{}, ErrRootUnconfirmed
	}
	request := executionRequest{ctx: ctx, runtime: &ticket, lease: lease, closeGuest: closeGuest, runtimeReply: make(chan runtimeGuestResult, 1)}
	select {
	case <-e.cleaned:
		return nil, childjob.GuestTokenExpectation{}, ErrRootUnconfirmed
	case <-ctx.Done():
		return nil, childjob.GuestTokenExpectation{}, ErrRootUnconfirmed
	case e.requests <- request:
	}
	select {
	case <-ctx.Done():
		return nil, childjob.GuestTokenExpectation{}, ErrRootUnconfirmed
	case <-e.cleaned:
		return nil, childjob.GuestTokenExpectation{}, ErrRootUnconfirmed
	case result := <-request.runtimeReply:
		if !runtimeGuestLive(ctx) {
			return nil, childjob.GuestTokenExpectation{}, ErrRootUnconfirmed
		}
		return result.prepared, result.expectation, result.err
	}
}

func (e *Execution) runtimeOnOwner(ctx context.Context, ticket servicehost.RuntimeGuestTicket, lease *serviceready.InspectionLease, closeGuest bool) (result runtimeGuestResult) {
	defer func() {
		if recover() != nil {
			result.err = ErrRootFatal
		}
	}()
	stage, ok := e.stage.(runtimeGuestStage)
	if !ok || !e.runtimeGuest || !runtimeGuestLive(ctx) {
		result.err = ErrRootUnconfirmed
		return result
	}
	a := e.runtimeAttempt
	if a != nil && (a.ticket != ticket || a.lease != lease) {
		result.err = ErrRootUnconfirmed
		return result
	}
	if closeGuest {
		if a == nil {
			a = &runtimeGuestAttempt{ticket: ticket, lease: lease}
			e.runtimeAttempt = a
		}
		if a.closed {
			return result
		}
		result.err = stage.CloseRuntimeGuest(ctx, ticket, e, lease)
		if result.err == nil && runtimeGuestLive(ctx) {
			a.prepared = nil
			a.expectation = childjob.GuestTokenExpectation{}
			a.closed = true
		} else if result.err == nil {
			result.err = ErrRootUnconfirmed
		}
		return result
	}
	if a != nil {
		result.err = ErrRootDenied
		return result
	}
	a = &runtimeGuestAttempt{ticket: ticket, lease: lease}
	e.runtimeAttempt = a // keep identity before invoking any fallible stage work
	result.prepared, result.expectation, result.err = stage.PrepareRuntimeGuest(ctx, ticket, e, lease)
	a.prepared = result.prepared // retain a partial before inspecting the result or context
	a.expectation = result.expectation
	if result.err == nil && (result.prepared == nil || !runtimeGuestLive(ctx)) {
		result.err = ErrRootUnconfirmed
	}
	return result
}
