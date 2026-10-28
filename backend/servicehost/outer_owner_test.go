package servicehost

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

type testOuterFinalizer func(context.Context, InnerCleanupProof) error

func (f testOuterFinalizer) Finalize(ctx context.Context, proof InnerCleanupProof) error {
	return f(ctx, proof)
}

func TestSCMOuterAcknowledgementKeepsStopPendingAndRetriesWithoutAnotherControl(t *testing.T) {
	entered := make(chan struct{})
	innerClosed := atomic.Bool{}
	ready := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, close: func(context.Context) error { innerClosed.Store(true); return nil }}
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		close(entered)
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	service, err := newService(ServiceConfig{Provisioning: source, Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: 100 * time.Millisecond, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return ready, nil })
	if err != nil {
		t.Fatal(err)
	}
	service.callerGuard = func(context.Context) error { return nil }
	released := atomic.Bool{}
	attempted := make(chan struct{}, 1)
	var attempts atomic.Int32
	if err := service.BindOuterFinalizer(testOuterFinalizer(func(_ context.Context, proof InnerCleanupProof) error {
		if !innerClosed.Load() || !proof.Confirms(service) {
			t.Error("outer close before inner ownership proof")
		}
		attempts.Add(1)
		select {
		case attempted <- struct{}{}:
		default:
		}
		if !released.Load() {
			return ErrRuntimeUnconfirmed
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	controls := make(chan Control, 1)
	states := make(chan ServiceState, 64)
	done := make(chan uint32, 1)
	go func() {
		specific, code := serveReadySCM(service, controls, states, func(c Control) Control { return c }, func(s ServiceState) ServiceState { return s })
		if specific {
			code++
		}
		done <- code
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no zero-grant ready")
	}
	controls <- Stop
	select {
	case <-attempted:
	case <-time.After(2 * time.Second):
		t.Fatal("outer close not requested")
	}
	if s := service.Snapshot(); s.State != StopPending || s.CleanupConfirmed || s.HasAuthority {
		t.Fatalf("outer resources reported stopped: %+v", s)
	}
	select {
	case code := <-done:
		t.Fatalf("Execute returned before outer acknowledgement: %d", code)
	case <-time.After(20 * time.Millisecond):
	}
	released.Store(true)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("recovered owner exit=%d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup required another SCM Stop")
	}
	if !service.Snapshot().CleanupConfirmed || service.Snapshot().State != Stopped || attempts.Load() < 2 {
		t.Fatal("outer acknowledgement not joined")
	}
}
