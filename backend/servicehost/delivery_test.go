package servicehost

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"sync/atomic"
	"testing"
	"time"
)

func deliveryTestService(t *testing.T, launcher Launcher, closePlane func(context.Context) error) (*Service, <-chan error) {
	t.Helper()
	sourceEntered := make(chan struct{})
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		close(sourceEntered)
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, close: closePlane}
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: 100 * time.Millisecond, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	select {
	case <-sourceEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not confirm actual ready")
	}
	return s, done
}
func TestHostDeliveryRegisteredLateWorkerMustJoinBeforeServiceCleanup(t *testing.T) {
	var planeCloses atomic.Int32
	launcher := &lifetimeLauncher{wait: func(context.Context) error { t.Error("late canceled delivery executed"); return nil }, close: func(context.Context) error { return nil }}
	s, done := deliveryTestService(t, launcher, func(context.Context) error { planeCloses.Add(1); return nil })
	d, err := s.prepareLegacyHostDelivery(context.Background(), lifetimeApproved(t))
	if err != nil {
		t.Fatal(err)
	}
	var ran atomic.Bool
	t.Cleanup(func() {
		if ran.CompareAndSwap(false, true) {
			_ = s.runHostDelivery(d)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err = s.Close(ctx)
	cancel()
	if err == nil || s.Snapshot().CleanupConfirmed || planeCloses.Load() != 0 {
		t.Fatal("registered but unstarted Host worker was skipped by cleanup")
	}
	select {
	case <-done:
		t.Fatal("Serve returned before registered worker joined")
	default:
	}
	ran.Store(true)
	_ = s.runHostDelivery(d)
	fresh, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	if err := s.Close(fresh); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Generation != 0 || !s.Snapshot().CleanupConfirmed || planeCloses.Load() != 1 {
		t.Fatal("late canceled worker issued authority or lost same-owner cleanup")
	}
}

type deliveryObservedLaunch struct {
	authority  *supervisor.Authority
	issued     IssuedLaunch
	registered bool
}
type deliveryCaptureLauncher struct {
	*lifetimeLauncher
	service *Service
	started chan deliveryObservedLaunch
	calls   atomic.Int32
}

func (l *deliveryCaptureLauncher) StartIssued(ctx context.Context, a *supervisor.Authority, i IssuedLaunch) (Process, error) {
	l.calls.Add(1)
	s := l.service
	s.mu.Lock()
	d := s.delivery
	registered := d != nil && d.owner == s && d.issuer == s.issuer && d.host == s.host && d.started
	s.mu.Unlock()
	p, err := l.lifetimeLauncher.StartIssued(ctx, a, i)
	l.started <- deliveryObservedLaunch{a, i, registered}
	return p, err
}
func TestHostDeliveryLegacyApprovalRegistersBeforeActualHostRunAndBorrowsIssuer(t *testing.T) {
	policy := lifetimeApproved(t)
	var reads, closes atomic.Int32
	launched := make(chan deliveryObservedLaunch, 1)
	launcher := &deliveryCaptureLauncher{lifetimeLauncher: &lifetimeLauncher{wait: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, close: func(context.Context) error { closes.Add(1); return nil }}, started: launched}
	var s *Service
	source := lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) { reads.Add(1); return policy, nil })
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, close: func(context.Context) error {
		s.mu.Lock()
		d := s.delivery
		s.mu.Unlock()
		if d == nil {
			t.Error("plane closed without registered Host")
		} else {
			select {
			case <-d.done:
			default:
				t.Error("plane closed before actual Host.Run worker joined")
			}
		}
		if closes.Load() == 0 {
			t.Error("plane closed before Host process cleanup")
		}
		return nil
	}}
	var err error
	s, err = newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	launcher.service = s
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), nil, make(chan ServiceState, 16)) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	var observed deliveryObservedLaunch
	select {
	case observed = <-launched:
	case <-time.After(2 * time.Second):
		t.Fatal("trusted legacy source did not reach real Host.Run")
	}
	if !observed.registered {
		t.Fatal("actual Host.Run was published before its owner registration")
	}
	if observed.issued.InstanceEpoch() != s.Snapshot().InstanceEpoch || s.Snapshot().Generation != 1 || reads.Load() != 1 {
		t.Fatal("worker replaced issuer or reread approval")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Host worker did not join")
	}
	if observed.authority.Snapshot(time.Now()).State != supervisor.Revoked || !s.Snapshot().CleanupConfirmed {
		t.Fatal("joined worker remained active or lost cleanup")
	}
}

func TestHostDeliveryFinalCleanupFatalIsStickyAndDoesNotRetryOnServiceThread(t *testing.T) {
	var closes, planeCloses, locked, unlocked atomic.Int32
	launcher := &deliveryCaptureLauncher{lifetimeLauncher: &lifetimeLauncher{wait: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, close: func(context.Context) error {
		if closes.Add(1) == 1 {
			return bootstrapauth.ErrFatal
		}
		return nil
	}}, started: make(chan deliveryObservedLaunch, 1)}
	s, done := deliveryTestService(t, launcher, func(context.Context) error { planeCloses.Add(1); return nil })
	launcher.service = s
	d, err := s.prepareLegacyHostDelivery(context.Background(), lifetimeApproved(t))
	if err != nil {
		t.Fatal(err)
	}
	d.thread = serviceThreadOperations{lock: func() { locked.Add(1) }, unlock: func() { unlocked.Add(1) }}
	go s.runHostDelivery(d)
	select {
	case <-launcher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Host did not start")
	}
	s.stopFor(nil)
	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
		t.Fatal("fatal Host worker did not join")
	}
	s.mu.Lock()
	outcome, cause := d.outcome, s.stopCause
	s.mu.Unlock()
	if !fatalServiceThread(outcome) || !fatalServiceThread(cause) || locked.Load() != 1 || unlocked.Load() != 0 {
		t.Fatal("final cleanup fatal was lost or worker thread reused")
	}
	select {
	case err := <-done:
		if !errors.Is(err, bootstrapauth.ErrFatal) {
			t.Fatal("Serve lost final Host fatal")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fatal owner did not retire")
	}
	if closes.Load() != 1 || planeCloses.Load() != 0 || s.Snapshot().CleanupConfirmed || s.Snapshot().State != StopPending {
		t.Fatal("joined fatal Host became cleanup proof or retried on Service thread")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !fatalServiceThread(s.Close(ctx)) {
		t.Fatal("fatal Close was normalized")
	}
}

func TestHostDeliveryJoinedPartialProcessFailureRetainsHostUntilSameOwnerRecovery(t *testing.T) {
	var recoverable atomic.Bool
	var reads, closes, planeCloses atomic.Int32
	retried := make(chan struct{}, 1)
	launcher := &deliveryCaptureLauncher{lifetimeLauncher: &lifetimeLauncher{failure: errors.New("synthetic partial launch"), wait: func(context.Context) error { t.Error("partial process entered Wait"); return nil }, close: func(context.Context) error {
		n := closes.Add(1)
		if !recoverable.Load() {
			if n >= 2 {
				select {
				case retried <- struct{}{}:
				default:
				}
			}
			return ErrRuntimeUnconfirmed
		}
		return nil
	}}, started: make(chan deliveryObservedLaunch, 1)}
	policy := lifetimeApproved(t)
	source := lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) { reads.Add(1); return policy, nil })
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, close: func(context.Context) error { planeCloses.Add(1); return nil }}
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: 100 * time.Millisecond, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	launcher.service = s
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	t.Cleanup(func() {
		recoverable.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	select {
	case <-launcher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("partial Host not launched")
	}
	select {
	case <-retried:
	case <-time.After(2 * time.Second):
		t.Fatal("same owned partial process was not retried")
	}
	s.mu.Lock()
	host, d := s.host, s.delivery
	s.mu.Unlock()
	if host == nil || d == nil || d.host != host {
		t.Fatal("partial Host reference was replaced or lost")
	}
	select {
	case <-d.done:
	default:
		t.Fatal("partial process test did not join the real Host worker")
	}
	if planeCloses.Load() != 0 || s.Snapshot().CleanupConfirmed || s.Snapshot().State != StopPending {
		t.Fatal("worker join was mistaken for workload cleanup")
	}
	select {
	case <-done:
		t.Fatal("Service returned with unconfirmed partial process")
	default:
	}
	recoverable.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("partial launch failure was hidden")
		}
	case <-ctx.Done():
		t.Fatal("recovered process did not complete owner")
	}
	s.mu.Lock()
	retained := s.host == host && s.delivery == d
	s.mu.Unlock()
	if !retained || reads.Load() != 1 || launcher.calls.Load() != 1 || planeCloses.Load() != 1 || !s.Snapshot().CleanupConfirmed {
		t.Fatal("recovery replaced approval/Host or lost sameowner cleanup")
	}
}
func TestHostDeliveryRejectsControlledFallbackAndForeignOrDuplicateWorker(t *testing.T) {
	controlled, _, _, _, legacy := dispatchTestOwner(t, 4)
	if _, err := controlled.prepareLegacyHostDelivery(context.Background(), lifetimeApproved(t)); err == nil {
		t.Fatal("controlled mode accepted legacy policy fallback")
	}
	if legacy.Load() != 0 || controlled.Snapshot().Generation != 0 {
		t.Fatal("controlled delivery read legacy source or created grant")
	}
	launcher := &lifetimeLauncher{wait: func(context.Context) error { t.Error("canceled worker executed"); return nil }, close: func(context.Context) error { return nil }}
	s, _ := deliveryTestService(t, launcher, nil)
	other, _ := deliveryTestService(t, launcher, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = other.Close(ctx)
	})
	d, err := s.prepareLegacyHostDelivery(context.Background(), lifetimeApproved(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepareLegacyHostDelivery(context.Background(), lifetimeApproved(t)); err == nil {
		t.Fatal("second Host replaced registered owner")
	}
	if err := other.runHostDelivery(d); err == nil {
		t.Fatal("foreign Service consumed Host worker")
	}
	s.stopFor(nil)
	_ = s.runHostDelivery(d)
	if err := s.runHostDelivery(d); err == nil {
		t.Fatal("Host worker executed twice")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Generation != 0 {
		t.Fatal("late duplicate/foreign delivery created authority")
	}
	if _, err := s.prepareLegacyHostDelivery(context.Background(), lifetimeApproved(t)); err == nil {
		t.Fatal("stopped Service admitted another Host")
	}
}
