package servicehost

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// Regressions after the observed Serve/serial/deadline/wait red-green slices.
func TestRuntimeServiceBudgetsAreExplicitBoundedAndCopied(t *testing.T) {
	for _, budgets := range []RequestLoopBudgets{{}, {Request: time.Second}, {Wait: time.Second}, {-1, time.Second}, {time.Second, -1}, {2 * time.Second, time.Second}, {time.Minute, time.Minute + 1}, {time.Minute + 1, time.Minute + 1}} {
		if err := budgets.Validate(); err == nil {
			t.Fatal("invalid request budget validated", budgets)
		}
		if s, err := NewRuntimeService(previewConfigFixture(t), budgets); s != nil || err == nil {
			t.Fatal("runtime filled missing budgets or accepted an unbounded request", budgets)
		}
	}
	budgets := RequestLoopBudgets{time.Minute, time.Minute}
	s := runtimeLoopServiceFixture(t, budgets)
	budgets.Wait, budgets.Request = 0, 0
	if s.requestLoop.budgets.Validate() != nil || s.issuer != nil || s.started {
		t.Fatal("constructor borrowed mutable limits or acquired runtime authority")
	}
}

func TestRuntimeServiceStopRetainsLateEndpointAndOriginalCleanupWorker(t *testing.T) {
	s := runtimeLoopServiceFixture(t, RequestLoopBudgets{time.Second, time.Second})
	entered, release, cleanupAttempt := make(chan context.Context, 1), make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var ack, wrongWorker atomic.Bool
	var calls, closes atomic.Int32
	endpoint := &serviceready.Endpoint{}
	s.requestLoop.exchange = func(ctx context.Context) error {
		calls.Add(1)
		r, err := s.auth.reserveExchange(ctx)
		if err != nil {
			return err
		}
		return r.runOwner(func() error {
			worker := authFixtureGoroutine()
			_, err := r.prepareEndpointOps(r.worker, func(call context.Context) (serviceAuthEndpointOps, error) {
				entered <- call
				<-release
				return serviceAuthEndpointOps{concrete: endpoint, close: func(context.Context) error {
					if authFixtureGoroutine() != worker {
						wrongWorker.Store(true)
					}
					select {
					case cleanupAttempt <- struct{}{}:
					default:
					}
					if !ack.Load() {
						return ErrRuntimeUnconfirmed
					}
					closes.Add(1)
					return nil
				}}, ErrProvisioningRejected
			})
			return err
		})
	}
	served := serveRuntimeLoopFixture(t, s)
	var call context.Context
	select {
	case call = <-entered:
	case <-time.After(time.Second):
		t.Fatal("runtime loop did not acquire its endpoint")
	}
	short, end := context.WithTimeout(context.Background(), 15*time.Millisecond)
	if err := s.Close(short); err == nil || !s.issuer.Snapshot().Closed || call.Err() == nil || s.Snapshot().CleanupConfirmed {
		t.Error("Stop failed to revoke and retain the in-flight owner", err)
	}
	end()
	unblock()
	select {
	case <-cleanupAttempt:
	case <-time.After(time.Second):
		t.Fatal("late partial endpoint was not reclaimed")
	}
	s.mu.Lock()
	retained := s.auth.active.endpoint != nil && s.auth.active.endpoint.ops.concrete == endpoint && !s.auth.active.cleaned
	s.mu.Unlock()
	if !retained {
		t.Error("late acquisition lost its original owner")
	}
	select {
	case <-served:
		t.Error("Serve returned without endpoint cleanup acknowledgement")
	default:
	}
	ack.Store(true)
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || closes.Load() != 1 || wrongWorker.Load() || !s.Snapshot().CleanupConfirmed {
		t.Fatal("cleanup changed workers, retried reception or lost completion")
	}
}

func TestRuntimeServiceRejectsSuccessfulCallbackWithoutOwnedExchange(t *testing.T) {
	s := runtimeLoopServiceFixture(t, RequestLoopBudgets{time.Second, time.Second})
	var calls atomic.Int32
	s.requestLoop.exchange = func(context.Context) error { calls.Add(1); return nil }
	served := serveRuntimeLoopFixture(t, s)
	select {
	case err := <-served:
		if err == nil || calls.Load() != 1 || s.auth.active != nil || s.auth.attempts != 0 || s.Snapshot().Generation != 0 {
			t.Fatal("callback success substituted for an owned exchange", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unacknowledged callback kept reception open")
	}
}

func TestRuntimeServiceReceptionPreservesLaunchLimit(t *testing.T) {
	s := runtimeLoopServiceFixture(t, RequestLoopBudgets{time.Second, time.Second})
	var calls atomic.Int32
	var hello servicecontrol.UntrustedResponse
	s.requestLoop.exchange = func(ctx context.Context) error {
		call := calls.Add(1)
		frame := dispatchHello
		if call > 1 {
			frame = dispatchRequest(hello, uint64(call-1), fmt.Sprintf("%032x", call), servicecontrol.Status)
		}
		return s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, frame), func(_ context.Context, raw []byte) error {
			reply, err := servicecontrol.DecodeUntrustedResponseFrame(raw)
			if call == 1 {
				hello = reply
			}
			return err
		})
	}
	served := serveRuntimeLoopFixture(t, s)
	select {
	case err := <-served:
		if calls.Load() != 4 || s.auth.attempts != 4 || !s.Snapshot().CleanupConfirmed || s.Snapshot().Generation != 0 {
			t.Fatal("runtime exceeded its launch bound or changed authority", calls.Load(), err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime retried after its launch bound")
	}
}

func TestPreviewServeRemainsQueryOnlyAndSupportsStatusQuiesce(t *testing.T) {
	s, err := NewPreviewService(previewConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	served := serveRuntimeLoopFixture(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for s.Snapshot().State != Running {
		select {
		case <-served:
			t.Fatal("preview unexpectedly started native reception")
		case <-ctx.Done():
			t.Fatal("preview did not become ready")
		case <-time.After(time.Millisecond):
		}
	}
	if s.requestLoop != nil {
		t.Fatal("preview adopted runtime reception")
	}
	var hello servicecontrol.UntrustedResponse
	for i, command := range []servicecontrol.Command{"", servicecontrol.Status, servicecontrol.RevokeAllAndQuiesce} {
		frame := dispatchHello
		if i > 0 {
			frame = dispatchRequest(hello, uint64(i), fmt.Sprintf("%032x", i+1), command)
		}
		if err := s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, frame), func(_ context.Context, raw []byte) error {
			reply, err := servicecontrol.DecodeUntrustedResponseFrame(raw)
			if i == 0 {
				hello = reply
			} else if i == 1 && reply.Snapshot == nil || i == 2 && reply.Quiesce == nil {
				t.Error("preview lost its query/quiesce response")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
		t.Fatal("preview created execution authority")
	}
}
