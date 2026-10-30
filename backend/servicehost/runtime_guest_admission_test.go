package servicehost

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func TestRuntimeGuestCanceledServiceParentClosesBeforeAnyAcquisition(t *testing.T) {
	s, err := NewPreviewService(previewConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	o := &runtimeGuestOwnerFixture{service: s}
	if err := s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if err := s.BindRuntimeGuestOwner(o); err != nil {
		t.Fatal(err)
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		t.Error("canceled startup acquired native Service state")
		return serviceControlWitness{}, ErrProvisioningRejected
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Serve(parent, nil, make(chan ServiceState, 32)); err == nil {
		t.Fatal("canceled Service parent started")
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.Close(cleanup); err != nil {
		t.Fatal("pre-acquisition cancellation left inner cleanup pending", err)
	}
	if !s.Snapshot().CleanupConfirmed || o.prepares.Load() != 0 || o.closes.Load() != 0 {
		t.Fatal("canceled startup acquired a guest or lost cleanup acknowledgement")
	}
}

func TestRuntimeGuestRequestCancellationRevokesCanonicalOwnerBeforeAdmission(t *testing.T) {
	s, o := runtimeGuestServiceFixture(t, nil, runtimeGuestKernelFixture{lease: &serviceready.InspectionLease{}})
	request, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	observed := make(chan bool, 1)
	o.prepare = func(admission context.Context, _ RuntimeGuestTicket, _ *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
		cancel() // cancellation wins before request watcher detachment
		<-admission.Done()
		check, end := context.WithTimeout(context.Background(), time.Second)
		defer end()
		observed <- s.issuer.Snapshot().Closed && s.authOwner.Admit(check) != nil
		return nil, childjob.GuestTokenExpectation{}, context.Canceled
	}
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	if err := s.auth.exchangeOps(request, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("cancellation detached into successful runtime admission")
	}
	if !<-observed || s.issuer.Snapshot().Generation != 0 {
		t.Fatal("actor cancellation preceded canonical issuer/auth revocation")
	}
}

func TestRuntimeGuestExpiredAdmissionRefusesCommitBeforeStopCallbackAcquiresGate(t *testing.T) {
	s, _ := runtimeGuestServiceFixture(t, nil)
	a := s.preview.runtimeGuest
	material := &runtimeGuestTargetFixture{protected: true}
	a.material.Store(&runtimeGuestMaterial{prepared: &childjob.Prepared{}, target: material}) // private operation fixture only
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	s.mu.Lock()
	a.cancelAdmission() // model the parent/deadline notification while canonical callback is waiting
	err := a.commit(ctx)
	s.mu.Unlock()
	if err == nil || material.commits.Load() != 0 {
		t.Fatal("expired admission executed before delayed canonical stop callback")
	}
}

// These are source context/timer observations, not physical Windows expiry or
// scheduling guarantees. Native Prepared lifetime remains tested in childjob.
func TestRuntimeGuestAdmissionIsBoundedByServiceParentAndFixedDeadline(t *testing.T) {
	for _, mode := range []string{"service-parent", "hard-deadline"} {
		t.Run(mode, func(t *testing.T) {
			c := previewConfigFixture(t)
			parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(3*time.Second))
			defer cancel()
			if mode == "hard-deadline" {
				c.Preview.HardDeadline = time.Now().Add(time.Second)
			}
			s, _ := runtimeGuestServiceFixtureWithParent(t, parent, c, nil)
			a := s.preview.runtimeGuest
			deadline, bounded := a.admission.Deadline()
			parentDeadline, _ := parent.Deadline()
			if !bounded || deadline.After(parentDeadline) || deadline.After(c.Preview.HardDeadline) {
				t.Fatal("admission extended the Service parent or fixed deadline")
			}
			if mode == "service-parent" {
				cancel()
			}
			wait, end := context.WithTimeout(context.Background(), 2*time.Second)
			defer end()
			select {
			case <-a.admission.Done():
			case <-wait.Done():
				t.Fatal("admission did not expire")
			}
			select {
			case <-s.authOwner.StopRequested():
			case <-wait.Done():
				t.Fatal("expired admission left canonical owner open")
			}
			if !s.issuer.Snapshot().Closed {
				t.Fatal("expired admission retained issuer authority")
			}
			if err := s.Close(wait); err != nil {
				t.Fatal(err)
			}
			select {
			case <-a.admissionWatch.done:
			default:
				t.Fatal("Close acknowledgement left admission callback unjoined")
			}
		})
	}
}
