package servicehost

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// This is a scheduling/ownership model over the existing private authenticated
// seam. It constructs no native Witness, Endpoint receipt, or input-match proof.
func TestControlFixtureExchangeRechecksOnSCMThreadBeforeAcquireAndReply(t *testing.T) {
	var scm atomic.Uint64
	var queries, closes, planeCalls atomic.Int32
	var wrong atomic.Bool
	s, err := newService(ServiceConfig{Provisioning: lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
		t.Error("controlled fixture read legacy approval")
		return ephemeral.ApprovedPolicy{}, ErrProvisioningRejected
	}), Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Hour}}, func(context.Context) (servicePlane, error) {
		planeCalls.Add(1)
		return nil, ErrProvisioningUnconfigured
	})
	if err != nil {
		t.Fatal(err)
	}
	s.control = &serviceControlReadiness{requests: make(chan serviceControlReadinessRequest, 1), prepare: func(context.Context) (serviceControlWitness, error) {
		scm.Store(authFixtureGoroutine())
		check := func() {
			if authFixtureGoroutine() != scm.Load() {
				wrong.Store(true)
			}
		}
		return serviceControlWitness{confirm: func(context.Context) error { check(); return nil }, recheck: func(context.Context) error { check(); queries.Add(1); return nil }, close: func(context.Context) error { check(); closes.Add(1); return nil }}, nil
	}}
	a, err := newServiceAuthAdapter(s, serviceDispatchProfile{ChannelNonce: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RequestLimit: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, end := context.WithTimeout(context.Background(), time.Second)
		defer end()
		_ = s.Close(cleanup)
	})
	for s.Snapshot().State != Running {
		select {
		case <-ctx.Done():
			t.Fatal("controlled fixture did not become ready")
		case <-time.After(time.Millisecond):
		}
	}
	batch := fixtureAuthBatch(a, dispatchHello)
	batch.start = func(context.Context) error {
		if queries.Load() != 2 {
			t.Error("Start lacked fresh SCM observation", queries.Load())
		}
		if authFixtureGoroutine() == scm.Load() {
			t.Error("authentication ran on SCM owner")
		}
		return nil
	}
	writes := 0
	if err := a.exchangeOps(ctx, batch, func(_ context.Context, frame []byte) error {
		writes++
		if queries.Load() != 3 {
			t.Error("reply lacked fresh SCM observation", queries.Load())
		}
		response, err := servicecontrol.DecodeUntrustedResponseFrame(frame)
		if err != nil || response.Type != "hello" {
			t.Errorf("wrong exact reply: %+v %v", response, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	select {
	case <-served:
	case <-cleanup.Done():
		t.Fatal("SCM owner did not join")
	}
	if writes != 1 || queries.Load() != 3 || closes.Load() != 1 || planeCalls.Load() != 0 || wrong.Load() {
		t.Fatal("controlled ownership/order changed", writes, queries.Load(), closes.Load(), planeCalls.Load(), wrong.Load())
	}
	if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority || !s.Snapshot().CleanupConfirmed {
		t.Fatal("fixture readiness minted authority or lost cleanup")
	}
}

// Observe is deliberately long only in this model fixture, so explicit readiness
// requests can be distinguished from periodic queries. Production is unchanged.
func controlReadinessFixture(t *testing.T, recheck func(context.Context) error) (*Service, *serviceAuthAdapter, <-chan error) {
	t.Helper()
	s, err := newService(ServiceConfig{Provisioning: lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
		return ephemeral.ApprovedPolicy{}, ErrProvisioningRejected
	}), Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Hour}}, func(context.Context) (servicePlane, error) {
		t.Error("controlled path used legacy Plane")
		return nil, ErrProvisioningUnconfigured
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.control = &serviceControlReadiness{requests: make(chan serviceControlReadinessRequest, 1), prepare: func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(c context.Context) error {
			if calls.Add(1) == 1 {
				return nil
			}
			return recheck(c)
		}, close: func(context.Context) error { return nil }}, nil
	}}
	a, err := newServiceAuthAdapter(s, serviceDispatchProfile{ChannelNonce: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RequestLimit: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for s.Snapshot().State != Running {
		select {
		case <-deadline.C:
			t.Fatal("fixture readiness missing")
		case <-time.After(time.Millisecond):
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s, a, done
}

func TestControlReadinessRequestHonorsActualDeadlineWhileSCMQueryWaits(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	outer, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, a, _ := controlReadinessFixture(t, func(context.Context) error {
		close(entered)
		select {
		case <-release:
		case <-outer.Done():
		}
		return nil
	})
	r, err := a.register(outer, fixtureAuthBatch(a, dispatchHello))
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan error, 1)
	ended := make(chan error, 1)
	go func() {
		ended <- r.runOwner(func() error {
			ctx := authDelayedDeadline{Context: context.Background(), deadline: time.Now().Add(10 * time.Millisecond)}
			_, err := r.requestControlObservation(ctx, controlBeforeAcquire)
			observed <- err
			return ErrProvisioningRejected
		})
	}()
	select {
	case <-entered:
	case <-outer.Done():
		t.Fatal("SCM request did not enter bounded memory barrier")
	}
	bounded := true
	select {
	case err := <-observed:
		if err == nil {
			t.Error("elapsed observation succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		bounded = false
	}
	close(release)
	if !bounded {
		<-observed
	}
	select {
	case <-ended:
	case <-outer.Done():
		t.Fatal("worker did not join")
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	if !bounded {
		t.Fatal("readiness request exceeded its actual finite deadline")
	}
}

func TestControlSCMRequestCannotMintSecondObservationBeforeFirstIsConsumed(t *testing.T) {
	var queries atomic.Int32
	s, a, _ := controlReadinessFixture(t, func(context.Context) error { queries.Add(1); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.register(ctx, fixtureAuthBatch(a, dispatchHello))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- r.runOwner(func() error {
			observation, err := r.requestControlObservation(r.worker, controlBeforeAcquire)
			if err != nil {
				return err
			}
			s.control.requests <- serviceControlReadinessRequest{call: observation.call}
			select {
			case replay := <-observation.call.reply:
				if replay.err == nil || replay.observation != nil {
					t.Error("SCM replay minted another observation")
				}
			case <-ctx.Done():
				t.Error("SCM replay did not return a bounded rejection")
			}
			if queries.Load() != 1 {
				t.Error("SCM replay repeated native recheck", queries.Load())
			}
			return ErrProvisioningRejected
		})
	}()
	select {
	case <-result:
	case <-ctx.Done():
		t.Fatal("observation worker did not join")
	}
}

func TestControlObservationRejectsWrongUseStopAndExpiredConsumption(t *testing.T) {
	for _, kind := range []string{"purpose", "copy", "foreign-request", "replay", "expired", "stop"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _ := controlReadinessFixture(t, func(context.Context) error { return nil })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, err := a.register(ctx, fixtureAuthBatch(a, dispatchHello))
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				result <- r.runOwner(func() error {
					observeCtx := r.worker
					end := func() {}
					if kind == "expired" {
						observeCtx, end = context.WithTimeout(r.worker, 20*time.Millisecond)
					}
					defer end()
					observation, err := r.requestControlObservation(observeCtx, controlBeforeAcquire)
					if err != nil {
						return err
					}
					switch kind {
					case "purpose":
						if r.consumeControlObservation(observation, controlBeforeDispatch) == nil {
							t.Error("wrong purpose consumed observation")
						}
					case "copy":
						copied := *observation
						if r.consumeControlObservation(&copied, controlBeforeAcquire) == nil {
							t.Error("copy consumed observation")
						}
					case "foreign-request":
						foreign := &serviceAuthRequest{adapter: r.adapter}
						if foreign.consumeControlObservation(observation, controlBeforeAcquire) == nil {
							t.Error("another request consumed observation")
						}
					case "expired":
						<-observeCtx.Done()
					case "stop":
						s.requestStop()
					}
					consumeErr := r.consumeControlObservation(observation, controlBeforeAcquire)
					if kind == "expired" || kind == "stop" {
						if consumeErr == nil {
							t.Error("stale observation consumed")
						}
					} else if consumeErr != nil {
						t.Error("original observation rejected", consumeErr)
					}
					if r.consumeControlObservation(observation, controlBeforeAcquire) == nil {
						t.Error("observation replay succeeded")
					}
					if _, err := r.requestControlObservation(r.worker, controlBeforeAcquire); err == nil {
						t.Error("used request purpose was reminted")
					}
					return ErrProvisioningRejected
				})
			}()
			select {
			case <-result:
			case <-ctx.Done():
				t.Fatal("observation worker did not join")
			}
		})
	}
}

func TestControlWitnessFailureNeverStartsAuthenticationOrWrites(t *testing.T) {
	s, a, _ := controlReadinessFixture(t, func(context.Context) error { return ErrProvisioningRejected })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch := fixtureAuthBatch(a, dispatchHello)
	starts, writes := 0, 0
	batch.start = func(context.Context) error { starts++; return nil }
	if err := a.exchangeOps(ctx, batch, func(context.Context, []byte) error { writes++; return nil }); err == nil {
		t.Fatal("missing fresh SCM observation succeeded")
	}
	if starts != 0 || writes != 0 || !s.issuer.Snapshot().Closed || s.Snapshot().Generation != 0 {
		t.Fatal("witness failure reached authentication or minted authority", starts, writes)
	}
}

func TestControlledConstructorUsesFixedPairAndBoundsHelloPlusRequests(t *testing.T) {
	factories, err := newServiceNativeFactories(controlFactoryConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := ServiceConfig{Provisioning: lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
		return ephemeral.ApprovedPolicy{}, ErrProvisioningRejected
	}), Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Second}}
	profile := serviceDispatchProfile{ChannelNonce: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RequestLimit: 1}
	for _, limit := range []uint64{0, 1, 1025} {
		if s, err := newControlledService(cfg, factories, profile, limit); s != nil || err == nil {
			t.Fatal("unbounded or insufficient launch budget accepted", limit)
		}
	}
	if s, err := newControlledService(cfg, nil, profile, 2); s != nil || err == nil {
		t.Fatal("missing fixed pair accepted")
	}
	copied := *factories
	if s, err := newControlledService(cfg, &copied, profile, 2); s != nil || err == nil {
		t.Fatal("copied fixed pair accepted")
	}
	s, err := newControlledService(cfg, factories, profile, 2)
	if err != nil || s == nil || s.control == nil || s.control.factories != factories || s.auth == nil || s.plane != nil {
		t.Fatal("explicit controlled composition missing", err)
	}
	// Construction alone performs no Witness/Endpoint acquisition or authentication.
	if s.started || s.control.prepared || s.issuer != nil {
		t.Fatal("controlled constructor acquired authority or native readiness")
	}
}
