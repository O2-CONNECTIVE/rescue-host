package servicehost

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewOuterBindingRetainsConstructionGuards(t *testing.T) {
	finalizer := testOuterFinalizer(func(context.Context, InnerCleanupProof) error { return nil })
	var zero Service
	if err := zero.BindOuterFinalizer(finalizer); !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatal("zero Service accepted an outer owner", err)
	}
	c := previewConfigFixture(t)
	if s, err := NewService(ServiceConfig{Ready: c.Ready, Budgets: c.Budgets}); s != nil || !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatal("legacy constructor accepted absent approval and launcher", err)
	}
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindOuterFinalizer(nil); !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatal("preview accepted an absent outer owner", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.BindOuterFinalizer(finalizer); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("unused but closed preview accepted an outer owner", err)
	}
}

// NewPreviewService is real. Only its existing private SCM caller/Witness
// operation seams are memory models; they establish no native authentication.
func TestPreviewSCMWaitsForSameOuterOwnerAcknowledgement(t *testing.T) {
	c := previewConfigFixture(t)
	c.Budgets.Observe = time.Millisecond
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	var prepared, witnessClosed, attempts atomic.Int32
	var released, acknowledged atomic.Bool
	firstAttempt := make(chan InnerCleanupProof, 1)
	var firstProof InnerCleanupProof
	finalizer := testOuterFinalizer(func(_ context.Context, proof InnerCleanupProof) error {
		if !proof.Confirms(s) || witnessClosed.Load() != 1 {
			t.Error("outer owner ran before the same Service's inner Witness closed")
		}
		if attempts.Add(1) == 1 {
			firstProof = proof
			firstAttempt <- proof
			return ErrRuntimeUnconfirmed
		}
		if proof != firstProof {
			t.Error("retry replaced the original inner cleanup proof")
		}
		if !released.Load() {
			return ErrRuntimeUnconfirmed
		}
		acknowledged.CompareAndSwap(false, true)
		return nil
	})
	if err := s.BindOuterFinalizer(finalizer); err != nil {
		t.Fatal("constructed preview rejected its outer owner", err)
	}
	if err := s.BindOuterFinalizer(finalizer); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("preview accepted a second outer owner", err)
	}
	s.callerGuard = func(context.Context) error { return nil }
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		prepared.Add(1)
		return serviceControlWitness{
			confirm: func(context.Context) error { return nil },
			recheck: func(context.Context) error { return nil },
			close:   func(context.Context) error { witnessClosed.Add(1); return nil },
		}, nil
	}
	controls, states := make(chan Control, 1), make(chan ServiceState, 64)
	type outcome struct {
		specific bool
		code     uint32
	}
	done, joined := make(chan outcome, 1), make(chan struct{})
	go func() {
		defer close(joined)
		specific, code := serveReadySCM(s, controls, states, func(c Control) Control { return c }, func(s ServiceState) ServiceState { return s })
		done <- outcome{specific, code}
	}()
	t.Cleanup(func() {
		released.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
		select {
		case <-joined:
		case <-ctx.Done():
			t.Error("preview SCM worker did not join")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for running := false; !running; {
		select {
		case state := <-states:
			running = state == Running
		case result := <-done:
			t.Fatal("preview SCM ended before Running", result)
		case <-ctx.Done():
			t.Fatal("preview SCM did not reach Running")
		}
	}
	if err := s.BindOuterFinalizer(finalizer); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("running preview accepted an outer owner", err)
	}
	controls <- Stop
	select {
	case proof := <-firstAttempt:
		if !proof.Confirms(s) || proof.Confirms(&Service{}) {
			t.Fatal("preview inner proof did not bind the same constructed Service")
		}
	case <-ctx.Done():
		t.Fatal("preview did not request outer acknowledgement")
	}
	if snapshot := s.Snapshot(); snapshot.State != StopPending || snapshot.CleanupConfirmed || snapshot.HasAuthority || snapshot.Generation != 0 || acknowledged.Load() {
		t.Fatalf("preview reported cleanup before outer acknowledgement: %+v", snapshot)
	}
	select {
	case result := <-done:
		t.Fatal("preview SCM returned before outer acknowledgement", result)
	default:
	}
	for len(states) > 0 {
		if state := <-states; state == Stopped {
			t.Fatal("preview published Stopped before outer acknowledgement")
		}
	}
	released.Store(true)
	select {
	case result := <-done:
		if result.specific || result.code != 0 {
			t.Fatal("acknowledged preview SCM failed", result)
		}
	case <-ctx.Done():
		t.Fatal("preview required another Stop to join the same outer owner")
	}
	before := attempts.Load()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot := s.Snapshot(); snapshot.State != Stopped || !snapshot.CleanupConfirmed || snapshot.HasAuthority || snapshot.Generation != 0 || !acknowledged.Load() || before < 2 || attempts.Load() != before || prepared.Load() != 1 || witnessClosed.Load() != 1 {
		t.Fatal("preview lost ownership or repeated acknowledged outer cleanup", snapshot)
	}
	if s.cfg.Provisioning != nil || s.cfg.Launcher != nil || s.prepare != nil || s.host != nil || s.reservation != nil || s.ownerIntent != nil {
		t.Fatal("preview acquired legacy activation dependencies or workload ownership")
	}
	if err := s.BindOuterFinalizer(finalizer); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("stopped preview accepted an outer owner", err)
	}
}
