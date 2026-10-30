package servicehost

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// Only the existing physical-operation and auth seams are modeled. The binder,
// confirmation, response completion, issuer and target continuation are real.
func runtimeEvaluationServiceFixture(t *testing.T) (*Service, *runtimeGuestTargetFixture) {
	t.Helper()
	c := previewConfigFixture(t)
	c.RequestLimit, c.LaunchLimit = 8, 9
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	o := &runtimeGuestOwnerFixture{service: s}
	if err = s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if err = s.BindRuntimeGuestEvaluationOwner(o); err != nil {
		t.Fatal(err)
	}
	const sid = "S-1-5-21-7-8-9-2048"
	target := &runtimeGuestTargetFixture{sid: sid, protected: true}
	s.preview.runtimeGuest.kernel = runtimeGuestKernelFixture{lease: &serviceready.InspectionLease{}, target: target}
	o.prepare = func(ctx context.Context, ticket RuntimeGuestTicket, _ *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
		d, err := ticket.DescriptorFor(ctx, o)
		if err == nil {
			err = ticket.BindCreatedGuest(ctx, o, d.Namespace, sid)
		}
		return &childjob.Prepared{}, childjob.GuestTokenExpectation{}, err
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	s.cfg.Budgets.Observe = time.Hour
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for s.Snapshot().State != Running {
		select {
		case <-ctx.Done():
			t.Fatal("evaluation Service did not start")
		case <-time.After(time.Millisecond):
		}
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := s.Close(cleanup); err != nil {
			t.Error(err)
		}
		select {
		case <-done:
		case <-cleanup.Done():
			t.Error("evaluation Service did not join")
		}
	})
	return s, target
}

func TestRuntimeEvaluationStartsOnlyAfterConfirmationWrite(t *testing.T) {
	s, target := runtimeEvaluationServiceFixture(t)
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	writes := 0
	err := s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error {
		writes++
		if target.commits.Load() != 0 || s.issuer.Snapshot().Consumed {
			t.Error("target started before actual confirmation write completed")
		}
		return nil
	})
	if err != nil || writes != 1 || target.commits.Load() != 1 || !s.issuer.Snapshot().Consumed {
		t.Fatal("successful confirmation did not continue exactly once", err, writes, target.commits.Load())
	}
}
