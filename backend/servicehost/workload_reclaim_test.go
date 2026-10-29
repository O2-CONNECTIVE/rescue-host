package servicehost

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func TestServiceWorkloadReclaimPrecedesAuthOwnerJoin(t *testing.T) {
	var releaseAuth atomic.Bool
	s, a, serveDone := authTestService(t, 1)
	request, stopRequest := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopRequest()
	_, err := s.reserveOwnerIntent(request, reservationTestIntent(s, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	issuer := s.issuer
	s.mu.Unlock()
	entered := make(chan struct{})
	batch := serviceAuthBatchOps{issuer: issuer, collector: &operatorauth.Collector{},
		start: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() },
		collect: func(context.Context) (*operatorauth.Read, error) {
			t.Error("canceled helper collected authentication")
			return nil, ErrProvisioningRejected
		},
		consume: func(context.Context, *operatorauth.Read) ([]byte, error) {
			t.Error("canceled helper consumed authentication")
			return nil, ErrProvisioningRejected
		},
		close: func(ctx context.Context) error {
			if releaseAuth.Load() {
				return nil
			}
			return s.awaitWorkloadReclaim(ctx, issuer)

		},
	}
	exchanged := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		exchanged <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { t.Error("canceled helper replied"); return nil })
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("authentication owner did not start within its request budget")
	}
	t.Cleanup(func() {
		releaseAuth.Store(true)
		cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		_ = s.Close(cleanup)
	})
	cleanup, end := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer end()
	if err := s.Close(cleanup); err != nil {
		t.Fatal("auth owner prevented workload recovery", err)
	}
	select {
	case <-exchanged:
	case <-cleanup.Done():
		t.Fatal("auth owner did not join")
	}
	select {
	case <-serveDone:
	case <-cleanup.Done():
		t.Fatal("service did not join")
	}
	if !s.Snapshot().CleanupConfirmed || s.Snapshot().Generation != 0 {
		t.Fatal("workload cleanup lost same-issuer lifetime")
	}
}

func TestServiceWorkloadTimeoutStillRequestsAuthCleanup(t *testing.T) {
	s, a, serveDone := authTestService(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := s.reserveOwnerIntent(ctx, reservationTestIntent(s, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.consumeOwnerReservation(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.finishOwnerReservation(work) })
	entered, retried := make(chan struct{}), make(chan struct{}, 1)
	var calls atomic.Int32
	s.mu.Lock()
	issuer := s.issuer
	s.mu.Unlock()
	batch := serviceAuthBatchOps{issuer: issuer, collector: &operatorauth.Collector{},
		start: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() },
		collect: func(context.Context) (*operatorauth.Read, error) {
			t.Error("canceled helper collected authentication")
			return nil, ErrProvisioningRejected
		},
		consume: func(context.Context, *operatorauth.Read) ([]byte, error) {
			t.Error("canceled helper consumed authentication")
			return nil, ErrProvisioningRejected
		},
		close: func(context.Context) error {
			if calls.Add(1) == 1 {
				return ErrRuntimeUnconfirmed
			}
			select {
			case retried <- struct{}{}:
			default:
			}
			return nil
		},
	}
	exchanged := make(chan error, 1)
	go func() {
		exchanged <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { t.Error("canceled helper replied"); return nil })
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("authentication owner did not start within its request budget")
	}
	s.requestStop()
	select {
	case <-retried:
	case <-time.After(750 * time.Millisecond):
		t.Fatal("workload timeout starved independent auth owner cleanup")
	}
	select {
	case <-exchanged:
	case <-time.After(time.Second):
		t.Fatal("auth owner did not acknowledge independent helper cleanup")
	}
	select {
	case <-serveDone:
		t.Fatal("service returned before reservation worker acknowledgement")
	default:
	}
	if s.Snapshot().CleanupConfirmed {
		t.Fatal("auth cleanup became workload proof")
	}
	if err := s.finishOwnerReservation(work); err != nil {
		t.Fatal(err)
	}
	cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	if err := s.Close(cleanup); err != nil {
		t.Fatal(err)
	}
}

func TestServiceWorkloadFatalNotifiesAuthOwnerWithoutCleanupProof(t *testing.T) {
	var planeCloses, hostCloses, outerCloses atomic.Int32
	s, err := newService(ServiceConfig{
		Provisioning: lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
			t.Error("controlled service read legacy approval")
			return ephemeral.ApprovedPolicy{}, ErrProvisioningRejected
		}),
		Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: 200 * time.Millisecond, Observe: time.Millisecond},
	}, func(context.Context) (servicePlane, error) {
		return &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, close: func(context.Context) error { planeCloses.Add(1); return nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := newServiceAuthAdapter(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: 4}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindOuterFinalizer(testOuterFinalizer(func(context.Context, InnerCleanupProof) error { outerCloses.Add(1); return nil })); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	ready := time.NewTimer(time.Second)
	defer ready.Stop()
	for s.Snapshot().State != Running {
		select {
		case <-ready.C:
			t.Fatal("service not ready")
		case <-time.After(time.Millisecond):
		}
	}
	// Memory fixture of an already joined delivery. Existing native-thread tests
	// verify that the original Host.Run owner closes its startup scope first.
	h := New(nil)
	h.started = true
	h.process = &stdioSyntheticProcess{close: func(context.Context) error { hostCloses.Add(1); return nil }}
	close(h.done)
	s.mu.Lock()
	issuer := s.issuer
	d := &serviceHostDelivery{owner: s, issuer: issuer, host: h, done: make(chan struct{}), finished: true, outcome: errors.Join(protectedkey.ErrFatal, errors.New("private workload detail"))}
	close(d.done)
	s.host, s.delivery = h, d
	s.mu.Unlock()
	entered := make(chan struct{})
	batch := serviceAuthBatchOps{issuer: issuer, collector: &operatorauth.Collector{},
		start: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() },
		collect: func(context.Context) (*operatorauth.Read, error) {
			t.Error("canceled helper collected authentication")
			return nil, ErrProvisioningRejected
		},
		consume: func(context.Context, *operatorauth.Read) ([]byte, error) {
			t.Error("canceled helper consumed authentication")
			return nil, ErrProvisioningRejected
		},
		close: func(ctx context.Context) error { return s.awaitWorkloadReclaim(ctx, issuer) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	exchanged := make(chan error, 1)
	go func() {
		exchanged <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { t.Error("canceled helper replied"); return nil })
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("authentication owner did not start within its request budget")
	}
	s.requestStop()
	select {
	case err := <-exchanged:
		if !errors.Is(err, protectedkey.ErrFatal) || strings.Contains(err.Error(), "private workload detail") {
			t.Fatal("auth owner lost workload fatal or leaked detail", err)
		}
	case <-ctx.Done():
		t.Fatal("auth owner did not receive terminal workload outcome")
	}
	select {
	case err := <-serveDone:
		if !errors.Is(err, protectedkey.ErrFatal) {
			t.Fatal("service lost terminal fatal", err)
		}
	case <-ctx.Done():
		t.Fatal("service awaited a fatal owner's nonexistent cleanup acknowledgement")
	}
	s.mu.Lock()
	r := a.active
	select {
	case <-r.ownerDone:
	default:
		t.Error("service returned before auth worker joined")
	}
	select {
	case <-r.done:
		t.Error("fatal owner produced a cleanup acknowledgement")
	default:
	}
	proof := InnerCleanupProof{service: s, ticket: s.outerTicket}
	s.mu.Unlock()
	if proof.Confirms(s) || planeCloses.Load() != 0 || outerCloses.Load() != 0 || hostCloses.Load() != 0 || s.Snapshot().CleanupConfirmed || s.Snapshot().State != StopPending {
		t.Fatal("terminal workload retried native cleanup or became whole-service proof")
	}
	if err := s.Close(ctx); !errors.Is(err, protectedkey.ErrFatal) {
		t.Fatal("fatal was not sticky", err)
	}
}

func TestServiceWorkloadReceiptRequiresWorkerAckAndExactIssuer(t *testing.T) {
	s, _, serveDone := authTestService(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := s.reserveOwnerIntent(ctx, reservationTestIntent(s, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.consumeOwnerReservation(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.finishOwnerReservation(work) })
	s.mu.Lock()
	issuer := s.issuer
	s.mu.Unlock()
	short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := s.Close(short); err == nil {
		t.Fatal("nil Host became cleanup before claimed worker acknowledgement")
	}
	end()
	short, end = context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := s.awaitWorkloadReclaim(short, issuer); err == nil {
		t.Fatal("cancellation became workload acknowledgement")
	}
	end()
	if s.Snapshot().CleanupConfirmed {
		t.Fatal("unjoined worker became whole-service proof")
	}
	if err := s.finishOwnerReservation(work); err != nil {
		t.Fatal(err)
	}
	if err := s.awaitWorkloadReclaim(ctx, issuer); err != nil {
		t.Fatal(err)
	}
	foreign, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := s.awaitWorkloadReclaim(ctx, foreign); err == nil {
		t.Fatal("foreign issuer consumed workload receipt")
	}
	elapsed := authDelayedDeadline{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	if err := s.awaitWorkloadReclaim(elapsed, issuer); err == nil {
		t.Fatal("elapsed deadline consumed cached workload receipt")
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- s.Close(ctx) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-serveDone:
	case <-ctx.Done():
		t.Fatal("same-owner recovery did not join service")
	}
	if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority || !s.Snapshot().CleanupConfirmed {
		t.Fatal("workload recovery fabricated authentication or authority")
	}
}
