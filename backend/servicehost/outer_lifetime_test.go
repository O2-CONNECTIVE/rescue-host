package servicehost

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func outerServiceFixture(t *testing.T) (*Service, *atomic.Int32) {
	t.Helper()
	prepared := &atomic.Int32{}
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	service, err := newService(ServiceConfig{Provisioning: source, Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: 50 * time.Millisecond, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) {
		prepared.Add(1)
		return &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	service.callerGuard = func(context.Context) error { return nil }
	return service, prepared
}
func outerTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSCMEarlyClosedServiceCannotReturnBeforeOuterAcknowledgement(t *testing.T) {
	service, prepared := outerServiceFixture(t)
	var released atomic.Bool
	if err := service.BindOuterFinalizer(testOuterFinalizer(func(_ context.Context, proof InnerCleanupProof) error {
		if !proof.Confirms(service) {
			t.Error("unused inner proof did not confirm")
		}
		if !released.Load() {
			return ErrRuntimeUnconfirmed
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(outerTestContext(t)); !errors.Is(err, ErrRuntimeUnconfirmed) {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		serveReadySCM(service, nil, nil, func(c Control) Control { return c }, func(s ServiceState) ServiceState { return s })
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("early Stopのouter ownerを確認する前にExecuteが戻りました")
	case <-time.After(20 * time.Millisecond):
	}
	released.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("early Stopの確認に追加SCM Stopが必要でした")
	}
	if !service.Snapshot().CleanupConfirmed || prepared.Load() != 0 {
		t.Fatal("early Closeがnative準備を作ったかouter確認を失いました")
	}
}

func TestInnerCleanupProofCannotReleaseOuterWhileApprovalBorrowerIsActive(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		close(entered)
		<-release
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	service, err := newService(ServiceConfig{Provisioning: source, Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: 10 * time.Millisecond, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) {
		return &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	proofs := make(chan InnerCleanupProof, 1)
	if err := service.BindOuterFinalizer(testOuterFinalizer(func(_ context.Context, p InnerCleanupProof) error { attempts.Add(1); proofs <- p; return nil })); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Serve(context.Background(), nil, make(chan ServiceState, 64)) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("approval borrower was not entered")
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = service.Close(short)
	cancel()
	if err == nil || attempts.Load() != 0 || service.Snapshot().CleanupConfirmed {
		t.Fatal("active approval borrower permitted outer cleanup")
	}
	if (InnerCleanupProof{}).Confirms(service) {
		t.Fatal("zero/self-claimed proof confirmed active service")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("inner owner required another Stop")
	}
	if service.Snapshot().State != StopPending || service.Snapshot().CleanupConfirmed {
		t.Fatal("inner completion became overall Stopped")
	}
	if err := service.Close(outerTestContext(t)); err != nil {
		t.Fatal(err)
	}
	proof := <-proofs
	other, _ := outerServiceFixture(t)
	if !proof.Confirms(service) || proof.Confirms(other) || proof.Confirms(nil) {
		t.Fatal("opaque proof was not bound to same Service")
	}
}

func TestDispatcherStartFailureAndConcurrentCloseShareOneOuterAcknowledgement(t *testing.T) {
	service, prepared := outerServiceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	if err := service.BindOuterFinalizer(testOuterFinalizer(func(ctx context.Context, p InnerCleanupProof) error {
		if !p.Confirms(service) {
			t.Error("start failure did not confirm empty inner ownership")
		}
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})); err != nil {
		t.Fatal(err)
	}
	dispatched := make(chan error, 1)
	go func() {
		dispatched <- runReadySCMDispatch(service, func() error { return errors.New("synthetic dispatcher failure") })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dispatcher failure did not finalize")
	}
	closing := make(chan error, 1)
	go func() { closing <- service.Close(outerTestContext(t)) }()
	select {
	case <-dispatched:
		t.Fatal("dispatcher error escaped before outer ack")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-closing; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-dispatched:
		if !errors.Is(err, ErrSCMUnavailable) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("start failure was not acknowledged")
	}
	if calls.Load() != 1 || prepared.Load() != 0 || !service.Snapshot().CleanupConfirmed {
		t.Fatal("concurrent caller duplicated finalizer or created native inner owner")
	}
}

func TestGuardFatalUsesOnlyEmptyInnerRPCAndDoesNotPrepare(t *testing.T) {
	service, prepared := outerServiceFixture(t)
	service.callerGuard = func(context.Context) error { return bootstrapauth.ErrFatal }
	var calls atomic.Int32
	if err := service.BindOuterFinalizer(testOuterFinalizer(func(_ context.Context, p InnerCleanupProof) error {
		calls.Add(1)
		if !p.Confirms(service) {
			t.Error("guard rejection had no empty inner proof")
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		specific, code := serveReadySCM(service, nil, nil, func(c Control) Control { return c }, func(s ServiceState) ServiceState { return s })
		done <- specific && code == 1
	}()
	select {
	case rejected := <-done:
		if !rejected {
			t.Fatal("Fatal guard was reported successful")
		}
	case <-time.After(time.Second):
		t.Fatal("empty inner cleanup did not join original outer owner")
	}
	if prepared.Load() != 0 || calls.Load() != 1 || !service.Snapshot().CleanupConfirmed {
		t.Fatal("guard rejection acquired inner native resources or lost outer ack")
	}
}

func TestInnerAndOuterFatalCannotMintAnotherCleanupProof(t *testing.T) {
	for _, inner := range []bool{false, true} {
		t.Run(map[bool]string{false: "outer", true: "inner"}[inner], func(t *testing.T) {
			var calls atomic.Int32
			service, _ := outerServiceFixture(t)
			if inner {
				service.cfg.Provisioning = lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
					return ephemeral.ApprovedPolicy{}, bootstrapauth.ErrFatal
				})
			}
			if err := service.BindOuterFinalizer(testOuterFinalizer(func(context.Context, InnerCleanupProof) error { calls.Add(1); return bootstrapauth.ErrFatal })); err != nil {
				t.Fatal(err)
			}
			if inner {
				done := make(chan error, 1)
				go func() { done <- service.Serve(context.Background(), nil, make(chan ServiceState, 64)) }()
				select {
				case err := <-done:
					if !fatalServiceThread(err) {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("Fatal inner owner did not retire")
				}
			}
			if err := service.Close(outerTestContext(t)); !fatalServiceThread(err) {
				t.Fatal("Fatal owner lost classification", err)
			}
			before := calls.Load()
			if err := service.Close(outerTestContext(t)); !fatalServiceThread(err) {
				t.Fatal("Fatal owner recovered")
			}
			if calls.Load() != before || inner && before != 0 || !inner && before != 1 || service.Snapshot().CleanupConfirmed || service.Snapshot().State != StopPending {
				t.Fatal("Fatal path re-finalized or reported Stopped")
			}
		})
	}
}

func TestOuterContextFailureRetainsSameOwnerForRetry(t *testing.T) {
	service, _ := outerServiceFixture(t)
	var calls atomic.Int32
	if err := service.BindOuterFinalizer(testOuterFinalizer(func(ctx context.Context, p InnerCleanupProof) error {
		if !p.Confirms(service) {
			t.Error("proof not confirmed")
		}
		if calls.Add(1) == 1 {
			<-ctx.Done()
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	err := service.Close(short)
	cancel()
	if err == nil || service.Snapshot().CleanupConfirmed || service.Snapshot().State != StopPending {
		t.Fatal("expired outer acknowledgement was accepted")
	}
	if err := service.Close(outerTestContext(t)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || !service.Snapshot().CleanupConfirmed {
		t.Fatal("same outer owner was not retained")
	}
}

func TestOuterFinalizerBindingRequiresConstructedUnusedService(t *testing.T) {
	callback := testOuterFinalizer(func(context.Context, InnerCleanupProof) error { return nil })
	var zero Service
	if err := zero.BindOuterFinalizer(callback); !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatal("unconfigured Service accepted an outer owner")
	}
	service, _ := outerServiceFixture(t)
	if err := service.BindOuterFinalizer(callback); err != nil {
		t.Fatal(err)
	}
	if err := service.BindOuterFinalizer(callback); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("duplicate outer owner binding accepted")
	}
	if err := service.Close(outerTestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := service.BindOuterFinalizer(callback); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("stopped service accepted a replacement outer owner")
	}
}
