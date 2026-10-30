package servicehost

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

type runtimeGuestOwnerFixture struct {
	service          *Service
	prepares, closes atomic.Int32
	resolve          func(context.Context, PreviewConfirmationTicket) error
	prepare          func(context.Context, RuntimeGuestTicket, *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error)
	closeGuest       func(context.Context, RuntimeGuestTicket, *serviceready.InspectionLease) error
}

func (o *runtimeGuestOwnerFixture) PrepareRuntimeGuest(ctx context.Context, ticket RuntimeGuestTicket, lease *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
	o.prepares.Add(1)
	if o.prepare != nil {
		return o.prepare(ctx, ticket, lease)
	}
	return nil, childjob.GuestTokenExpectation{}, ErrProvisioningUnconfigured
}
func (o *runtimeGuestOwnerFixture) CloseRuntimeGuest(ctx context.Context, ticket RuntimeGuestTicket, lease *serviceready.InspectionLease) error {
	o.closes.Add(1)
	if o.closeGuest != nil {
		return o.closeGuest(ctx, ticket, lease)
	}
	return nil
}
func (o *runtimeGuestOwnerFixture) ResolvePreviewConfirmation(ctx context.Context, ticket PreviewConfirmationTicket) error {
	if o.resolve != nil {
		return o.resolve(ctx, ticket)
	}
	_, err := ticket.PreviewFor(o.service)
	return err
}

func runtimeGuestServiceFixture(t *testing.T, resolve func(context.Context, PreviewConfirmationTicket) error, kernels ...runtimeGuestKernel) (*Service, *runtimeGuestOwnerFixture) {
	t.Helper()
	c := previewConfigFixture(t)
	c.RequestLimit, c.LaunchLimit = 8, 9
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	o := &runtimeGuestOwnerFixture{service: s, resolve: resolve}
	if err := s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if err := s.BindRuntimeGuestOwner(o); err != nil {
		t.Fatal(err)
	}
	if len(kernels) == 1 {
		s.preview.runtimeGuest.kernel = kernels[0]
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	s.cfg.Budgets.Observe = time.Hour
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	ctx, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	for s.Snapshot().State != Running {
		select {
		case <-ctx.Done():
			t.Fatal("runtime Service did not run")
		case <-time.After(time.Millisecond):
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Close(cleanup); err != nil {
			t.Error(err)
		}
		select {
		case <-done:
		case <-cleanup.Done():
			t.Error("runtime owner did not join")
		}
	})
	return s, o
}

func TestRuntimeGuestTicketRetainsLateCreationWithoutCreatingAuthority(t *testing.T) {
	var s *Service
	var o *runtimeGuestOwnerFixture
	var saved RuntimeGuestTicket
	s, o = runtimeGuestServiceFixture(t, func(ctx context.Context, confirmation PreviewConfirmationTicket) error {
		s.mu.Lock()
		a := s.preview.runtimeGuest
		r := &runtimeGuestRecord{actor: a, confirmation: confirmation.record, descriptor: GuestStagingDescriptor{Preview: s.preview.descriptor, Namespace: GuestStagingNamespace{Epoch: [32]byte{1}, Intent: [32]byte{2}, Generation: 1}}}
		r.self, a.record = r, r // fixed-owner receipt fixture, never a native producer
		s.mu.Unlock()
		saved = RuntimeGuestTicket{record: r}
		d, err := saved.DescriptorFor(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		d.Preview.AllowedKeyFingerprints[0] = "caller mutation"
		again, err := saved.DescriptorFor(ctx, o)
		if err != nil || again.Preview.AllowedKeyFingerprints[0] == "caller mutation" {
			t.Fatal("runtime descriptor was not frozen", err)
		}
		copy := *r
		if _, err := (RuntimeGuestTicket{record: &copy}).DescriptorFor(ctx, o); err == nil {
			t.Fatal("copied record observed runtime")
		}
		if _, err := saved.DescriptorFor(ctx, &runtimeGuestOwnerFixture{service: s}); err == nil {
			t.Fatal("foreign owner observed runtime")
		}
		s.requestStop()
		if _, err := saved.DescriptorFor(ctx, o); err == nil {
			t.Fatal("stop left acquisition open")
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := saved.BindCreatedGuest(canceled, o, again.Namespace, "S-1-5-21-7-8-9-2048"); err != nil {
			t.Fatal("late creation receipt was lost", err)
		}
		if err := saved.BindCreatedGuest(canceled, o, again.Namespace, "S-1-5-21-7-8-9-2049"); err == nil {
			t.Fatal("late receipt replaced the cleanup SID")
		}
		return ErrProvisioningRejected
	})
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("late receipt became confirmation success")
	}
	if saved.record == nil || saved.record.sid != "S-1-5-21-7-8-9-2048" || s.preview.runtime.owner.expectedSID != saved.record.sid || s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
		t.Fatal("receipt lost original target or created authority")
	}
}
func (o *runtimeGuestOwnerFixture) Finalize(_ context.Context, proof InnerCleanupProof) error {
	if !proof.Confirms(o.service) {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func TestRuntimeGuestOwnerRegistersFixedActorBeforeServiceAcquisition(t *testing.T) {
	s, err := NewPreviewService(previewConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	o := &runtimeGuestOwnerFixture{service: s}
	if err := s.BindRuntimeGuestOwner(o); err == nil {
		t.Fatal("runtime owner bound before outer owner")
	}
	if err := s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if err := s.BindRuntimeGuestOwner(&runtimeGuestOwnerFixture{service: s}); err == nil {
		t.Fatal("foreign outer owner bound")
	}
	if err := s.BindRuntimeGuestOwner(o); err != nil {
		t.Fatal(err)
	}
	if err := s.BindRuntimeGuestOwner(o); err != ErrAlreadyUsed {
		t.Fatal("runtime owner replaced", err)
	}
	if s.executionClosed || s.guestOwner != nil {
		t.Fatal("runtime path reused never-created staging")
	}
	observed := make(chan *confirmationTargetOwner, 1)
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		s.mu.Lock()
		registration := s.preview.targetOwner
		valid := registration != nil && registration.source == s.preview.runtime && registration.owner == registration.source.owner && registration.target == registration.source.target && registration.owner.target == registration.target && registration.kernel == registration.source.kernel && registration.owner.service == s && registration.owner.issuer == s.issuer && registration.owner.auth == s.authOwner && registration.owner.outer == s.outerTicket
		s.mu.Unlock()
		if !valid {
			t.Error("native acquisition preceded fixed runtime actor registration")
		}
		observed <- registration
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	s.cfg.Budgets.Observe = time.Hour
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	ctx, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("Service did not enter acquisition")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("runtime Service owner did not join")
	}
	if o.prepares.Load() != 0 || o.closes.Load() != 0 || s.Snapshot().Generation != 0 {
		t.Fatal("registration created runtime or authority")
	}
}
