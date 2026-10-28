package servicehost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

type lifetimePolicySource func(context.Context) (ephemeral.ApprovedPolicy, error)

func (f lifetimePolicySource) ReadApproved(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
	return f(ctx)
}

type lifetimeLauncher struct {
	a           *supervisor.Authority
	issued      IssuedLaunch
	wait, close func(context.Context) error
	failure     error
}

func (*lifetimeLauncher) Start(context.Context, *supervisor.Authority) (Process, error) {
	return nil, ErrActivationUnconfigured
}
func (l *lifetimeLauncher) StartIssued(_ context.Context, a *supervisor.Authority, i IssuedLaunch) (Process, error) {
	l.a, l.issued = a, i
	return &stdioSyntheticProcess{wait: l.wait, close: l.close}, l.failure
}
func lifetimeApproved(t *testing.T) ephemeral.ApprovedPolicy {
	t.Helper()
	_, g := nativeConfigFixture(t)
	return ephemeral.ApprovedPolicy{PolicyDigest: g.Binding.Identity.PolicyDigest, KeySetDigest: g.KeySetDigest, GuestBoundary: g.GuestBoundary, HardDeadline: g.HardDeadline, BackendLeaseCeiling: g.BackendLeaseCeiling}
}
func TestSessionRunnerBorrowsServiceIssuerWithoutReplacingOrClosingIt(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	policy := lifetimeApproved(t)
	launcher := &lifetimeLauncher{wait: func(context.Context) error { return errors.New("synthetic exit") }, close: func(context.Context) error { return nil }}
	h, err := WithRuntime(lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) { return policy, nil }), launcher, Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.borrowedIssuer = issuer
	_ = h.Run(context.Background())
	s := issuer.Snapshot()
	if launcher.a == nil || launcher.issued.InstanceEpoch() != s.InstanceEpoch || s.Generation != 1 {
		t.Fatal("runner replaced service issuer")
	}
	if s.Closed {
		t.Fatal("session runner closed service lifetime issuer")
	}
	if launcher.a.Snapshot(time.Now()).State != supervisor.Revoked {
		t.Fatal("finished runner retained its grant")
	}
}

type serviceMemoryPlane struct{ recheck, confirm, close func(context.Context) error }

func (p *serviceMemoryPlane) Recheck(ctx context.Context) error {
	if p.recheck != nil {
		return p.recheck(ctx)
	}
	return nil
}
func (p *serviceMemoryPlane) ConfirmRunning(ctx context.Context) error { return p.confirm(ctx) }
func (p *serviceMemoryPlane) Close(ctx context.Context) error {
	if p.close != nil {
		return p.close(ctx)
	}
	return nil
}

func TestServiceReadyHasZeroGrantsUntilActualRunningAndFreshApproval(t *testing.T) {
	approved := make(chan ephemeral.ApprovedPolicy)
	confirmed := make(chan struct{})
	sourceEntered := make(chan struct{})
	launched := make(chan *supervisor.Authority, 1)
	policy := lifetimeApproved(t)
	launcher := &lifetimeLauncher{wait: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, close: func(context.Context) error { return nil }}
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		close(sourceEntered)
		select {
		case p := <-approved:
			return p, nil
		case <-ctx.Done():
			return ephemeral.ApprovedPolicy{}, ctx.Err()
		}
	})
	observed := &serviceCaptureLauncher{lifetimeLauncher: launcher, launched: launched}
	var s *Service
	plane := &serviceMemoryPlane{confirm: func(ctx context.Context) error {
		select {
		case <-confirmed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, close: func(ctx context.Context) error {
		if s.Snapshot().HasAuthority {
			t.Error("plane cleanup preceded all revoke")
		}
		return nil
	}}
	var err error
	s, err = newService(ServiceConfig{Provisioning: source, Launcher: observed, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	controls := make(chan Control, 1)
	states := make(chan ServiceState, 4)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- s.Serve(ctx, controls, states) }()
	select {
	case state := <-states:
		if state != StartPending {
			t.Fatal(state)
		}
	case err := <-done:
		t.Fatalf("service did not prepare: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no start status")
	}
	select {
	case state := <-states:
		if state != Running {
			t.Fatal(state)
		}
	case err := <-done:
		t.Fatalf("service did not report running: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no running status")
	}
	snapshot := s.Snapshot()
	if snapshot.Generation != 0 || snapshot.HasAuthority || snapshot.InstanceEpoch == ([32]byte{}) {
		t.Fatal("ready created a grant")
	}
	select {
	case <-sourceEntered:
		t.Fatal("source called before actual running confirmation")
	default:
	}
	close(confirmed)
	select {
	case <-sourceEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("confirmed ready did not wait for explicit approval")
	}
	// Launcherの入口で発行instanceを観察し、同じauthorityを停止対象にする。
	approved <- policy
	var authority *supervisor.Authority
	select {
	case authority = <-launched:
	case err := <-done:
		t.Fatalf("approved launch rejected: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no approved launch")
	}
	if observed.issued.InstanceEpoch() != snapshot.InstanceEpoch || s.Snapshot().Generation != 1 {
		t.Fatal("launch replaced service issuer")
	}
	controls <- Stop
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not join")
	}
	if authority.Snapshot(time.Now()).State != supervisor.Revoked || !s.Snapshot().CleanupConfirmed || s.Snapshot().State != Stopped {
		t.Fatal("stop was not revoked and reclaimed")
	}
}

type serviceCaptureLauncher struct {
	*lifetimeLauncher
	launched chan<- *supervisor.Authority
}

func (l *serviceCaptureLauncher) StartIssued(ctx context.Context, a *supervisor.Authority, i IssuedLaunch) (Process, error) {
	p, err := l.lifetimeLauncher.StartIssued(ctx, a, i)
	l.launched <- a
	return p, err
}

func TestUnconfirmedServiceCleanupHoldsStopPendingUntilSameOwnerRetry(t *testing.T) {
	var recoverable atomic.Bool
	failed := make(chan struct{}, 1)
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, close: func(context.Context) error {
		if !recoverable.Load() {
			select {
			case failed <- struct{}{}:
			default:
			}
			return ErrRuntimeUnconfirmed
		}
		return nil
	}}
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	launcher := &lifetimeLauncher{wait: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	controls := make(chan Control, 1)
	states := make(chan ServiceState, 8)
	done := make(chan error, 1)
	t.Cleanup(func() {
		recoverable.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	go func() { done <- s.Serve(context.Background(), controls, states) }()
	for want := range []ServiceState{StartPending, Running} {
		select {
		case actual := <-states:
			if actual != []ServiceState{StartPending, Running}[want] {
				t.Fatal(actual)
			}
		case <-time.After(time.Second):
			t.Fatal("no ready status")
		}
	}
	controls <- Stop
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("cleanup not attempted")
	}
	select {
	case err := <-done:
		t.Fatalf("unconfirmed cleanup ended service owner: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	snapshot := s.Snapshot()
	if snapshot.State != StopPending || snapshot.CleanupConfirmed || snapshot.HasAuthority {
		t.Fatal("unconfirmed cleanup was reported stopped")
	}
	recoverable.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
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
		t.Fatal("same owner cleanup was not joined")
	}
	if !s.Snapshot().CleanupConfirmed || s.Snapshot().State != Stopped {
		t.Fatal("confirmed cleanup not reported")
	}
}

func TestSCMEntryRejectsOriginalCallerBeforeCreatingServiceOwner(t *testing.T) {
	source := lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
		t.Error("rejected caller reached approval")
		return ephemeral.ApprovedPolicy{}, ErrProvisioningRejected
	})
	launcher := &lifetimeLauncher{wait: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}
	prepared := 0
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { prepared++; return nil, ErrProvisioningRejected })
	if err != nil {
		t.Fatal(err)
	}
	guarded := 0
	s.callerGuard = func(context.Context) error { guarded++; return bootstrapauth.ErrFatal }
	specific, code := serveReadySCM(s, make(chan Control), make(chan ServiceState), func(c Control) Control { return c }, func(state ServiceState) ServiceState { return state })
	if guarded != 1 || prepared != 0 || !specific || code != 1 {
		t.Fatalf("original caller guard bypassed: guards=%d prepare=%d specific=%v code=%d", guarded, prepared, specific, code)
	}
	if !s.Snapshot().CleanupConfirmed || s.Snapshot().HasAuthority {
		t.Fatal("no-resource rejection was not closed")
	}
}

func TestSCMAdapterUsesConfirmedZeroGrantOwnerAndJoinsBeforeReturn(t *testing.T) {
	entered := make(chan struct{})
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		close(entered)
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	launcher := &lifetimeLauncher{wait: func(context.Context) error { t.Error("unapproved launch"); return nil }, close: func(context.Context) error { return nil }}
	var guarded atomic.Bool
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) {
		if !guarded.Load() {
			t.Error("SCM entry caller was not checked")
		}
		return plane, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.callerGuard = func(context.Context) error { guarded.Store(true); return nil }
	controls := make(chan Control, 1)
	states := make(chan ServiceState, 8)
	done := make(chan uint32, 1)
	go func() {
		specific, code := serveReadySCM(s, controls, states, func(c Control) Control { return c }, func(state ServiceState) ServiceState { return state })
		if specific {
			code++
		}
		done <- code
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no confirmed readiness")
	}
	if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
		t.Fatal("SCM ready issued authorization")
	}
	controls <- Stop
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("confirmed SCM stop failed: %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SCM did not join owner")
	}
	if !s.Snapshot().CleanupConfirmed {
		t.Fatal("SCM returned before cleanup confirmation")
	}
}

func TestServiceClosedBeforeStartCannotCreateAnotherIssuerOrListener(t *testing.T) {
	prepared := 0
	source := lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
		t.Error("closed service read approval")
		return ephemeral.ApprovedPolicy{}, ErrProvisioningRejected
	})
	launcher := &lifetimeLauncher{wait: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { prepared++; return nil, ErrProvisioningRejected })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("unused service cleanup failed: %v", err)
	}
	if err := s.Serve(context.Background(), nil, nil); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatalf("closed service restarted: %v", err)
	}
	if prepared != 0 || s.Snapshot().InstanceEpoch != ([32]byte{}) || !s.Snapshot().CleanupConfirmed || s.Snapshot().State != Stopped {
		t.Fatal("closed service was recreated")
	}
	if err := s.Close(ctx); err != nil || s.Snapshot().State != Stopped {
		t.Fatal("idempotent close regressed stopped state")
	}
}

func TestLaunchFailureClosesWholeServiceIssuerBeforePartialProcessCleanup(t *testing.T) {
	policy := lifetimeApproved(t)
	source := lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) { return policy, nil })
	var s *Service
	var revokedFirst atomic.Bool
	var first sync.Once
	launcher := &lifetimeLauncher{failure: errors.New("synthetic launch failure"), wait: func(context.Context) error { t.Error("failed launch waited for runtime"); return nil }, close: func(context.Context) error {
		snapshot := s.Snapshot()
		first.Do(func() { revokedFirst.Store(snapshot.Stopping && !snapshot.HasAuthority && s.issuer.Snapshot().Closed) })
		return nil
	}}
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}
	var err error
	s, err = newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	states := make(chan ServiceState, 8)
	if err := s.Serve(context.Background(), nil, states); err == nil {
		t.Fatal("launch failure was hidden")
	}
	if !revokedFirst.Load() {
		t.Fatal("partial process cleanup preceded whole-service issuance barrier")
	}
	if !s.Snapshot().CleanupConfirmed || s.Snapshot().State != Stopped {
		t.Fatal("failed service cleanup not confirmed")
	}
	if _, err := s.issuer.Prepare(policy); err == nil {
		t.Fatal("failed issuer admitted another approval")
	}
	if err := s.Serve(context.Background(), nil, states); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("failed service boot resumed")
	}
}

func TestServiceStopDuringReadinessRecheckRemainsAConfirmedStop(t *testing.T) {
	rechecking := make(chan struct{})
	var checks atomic.Int32
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, recheck: func(ctx context.Context) error {
		if checks.Add(1) == 1 {
			return nil
		}
		select {
		case <-rechecking:
		default:
			close(rechecking)
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	launcher := &lifetimeLauncher{wait: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	controls := make(chan Control, 1)
	states := make(chan ServiceState, 8)
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), controls, states) }()
	select {
	case <-rechecking:
	case <-time.After(time.Second):
		t.Fatal("readiness monitor did not run")
	}
	controls <- Stop
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean stop became readiness failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not join")
	}
	if !s.Snapshot().CleanupConfirmed || s.Snapshot().HasAuthority {
		t.Fatal("stop not confirmed")
	}
}

func TestApprovalCancellationRetainsFatalAndRetiresItsWorker(t *testing.T) {
	for _, fatal := range []error{bootstrapauth.ErrFatal, protectedkey.ErrFatal} {
		t.Run(fatal.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := lifetimeApproved(t)
			private := errors.New("synthetic-private-diagnostic")
			source := lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
				cancel()
				return policy, errors.Join(fatal, private)
			})
			launcher := &lifetimeLauncher{wait: func(context.Context) error { t.Error("canceled policy launched"); return nil }, close: func(context.Context) error { return nil }}
			plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}
			s, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
			if err != nil {
				t.Fatal(err)
			}
			var locked, unlocked atomic.Int32
			s.approvalThread = serviceThreadOperations{lock: func() { locked.Add(1) }, unlock: func() { unlocked.Add(1) }}
			err = s.Serve(ctx, nil, make(chan ServiceState, 8))
			if !errors.Is(err, fatal) || strings.Contains(err.Error(), private.Error()) {
				t.Fatalf("fatal classification lost or private diagnostic leaked: %v", err)
			}
			if locked.Load() != 1 || unlocked.Load() != 0 {
				t.Fatalf("fatal worker reused: lock=%d unlock=%d", locked.Load(), unlocked.Load())
			}
			if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
				t.Fatal("canceled policy was consumed")
			}
		})
	}
}
