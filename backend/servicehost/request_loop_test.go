package servicehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func TestRuntimeServiceConnectionWaitHasItsOwnDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := runtimeLoopServiceFixture(t, RequestLoopBudgets{Wait: 20 * time.Millisecond, Request: time.Second})
		entered := make(chan context.Context, 1)
		var closed atomic.Int32
		s.requestLoop.exchange = func(ctx context.Context) error {
			r, err := s.auth.reserveExchange(ctx)
			if err != nil {
				return err
			}
			return r.runOwner(func() error {
				_, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
					return serviceAuthEndpointOps{concrete: &serviceready.Endpoint{}, close: func(context.Context) error { closed.Add(1); return nil }, awaitConnected: func(wait context.Context) error {
						entered <- wait
						<-wait.Done()
						return wait.Err()
					}}, nil
				})
				if err != nil {
					return err
				}
				_, err = r.splitEndpointForInspection(r.worker)
				return err
			})
		}
		served := serveRuntimeLoopFixture(t, s)
		select {
		case wait := <-entered:
			deadline, ok := wait.Deadline()
			if !ok || time.Until(deadline) > s.requestLoop.budgets.Wait {
				t.Error("connection wait inherited the whole request budget")
			}
		case err := <-served:
			t.Fatal("runtime skipped its owned AwaitConnected operation", err)
		case <-time.After(time.Second):
			t.Fatal("runtime never awaited connection")
		}
		select {
		case <-served:
			if closed.Load() != 1 || !s.Snapshot().CleanupConfirmed || s.auth.attempts != 1 {
				t.Fatal("wait timeout retried or lost its endpoint owner")
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatal("connection wait used the request deadline instead of its shorter budget")
		}
	})
}

func TestRuntimeServiceDeadlineStopsAndRetainsLateRequestWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := runtimeLoopServiceFixture(t, RequestLoopBudgets{Wait: 10 * time.Millisecond, Request: 30 * time.Millisecond})
		entered, release := make(chan context.Context, 1), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		var starts, collects, writes, closes atomic.Int32
		s.requestLoop.exchange = func(ctx context.Context) error {
			batch := fixtureAuthBatch(s.auth, dispatchHello)
			batch.start = func(call context.Context) error {
				starts.Add(1)
				entered <- call
				<-release
				return nil
			}
			batch.collect = func(context.Context) (*operatorauth.Read, error) {
				collects.Add(1)
				return nil, ErrProvisioningRejected
			}
			batch.close = func(context.Context) error { closes.Add(1); return nil }
			return s.auth.exchangeOps(ctx, batch, func(context.Context, []byte) error { writes.Add(1); return nil })
		}
		served := serveRuntimeLoopFixture(t, s)
		var call context.Context
		select {
		case call = <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not reach Start")
		}
		<-call.Done()
		deadline := time.NewTimer(100 * time.Millisecond)
		defer deadline.Stop()
		for !s.Snapshot().Stopping {
			select {
			case <-deadline.C:
				t.Error("expired request did not stop the Service while its worker was held")
				goto stopped
			case <-time.After(time.Millisecond):
			}
		}
	stopped:
		short, end := context.WithTimeout(context.Background(), 15*time.Millisecond)
		if err := s.Close(short); err == nil || s.Snapshot().CleanupConfirmed {
			t.Error("deadline cancellation became worker completion", err)
		}
		end()
		select {
		case <-served:
			t.Error("Serve returned before the held request worker")
		default:
		}
		unblock()
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(cleanup); err != nil {
			t.Fatal(err)
		}
		if starts.Load() != 1 || collects.Load() != 0 || writes.Load() != 0 || closes.Load() != 1 {
			t.Fatal("late Start continued or original owner was not reclaimed", starts.Load(), collects.Load(), writes.Load(), closes.Load())
		}
	})
}

func runtimeLoopServiceFixture(t *testing.T, budgets RequestLoopBudgets) *Service {
	t.Helper()
	c := previewConfigFixture(t)
	c.Budgets.Cleanup, c.Budgets.Observe = 50*time.Millisecond, 10*time.Millisecond
	s, err := NewRuntimeService(c, budgets)
	if err != nil {
		t.Fatal(err)
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	return s
}

func serveRuntimeLoopFixture(t *testing.T, s *Service) <-chan error {
	t.Helper()
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return served
}

func TestRuntimeServiceSerialRequestsAwaitResponseAndOwnerAck(t *testing.T) {
	s := runtimeLoopServiceFixture(t, RequestLoopBudgets{Wait: time.Second, Request: time.Second})
	firstWrite, release, secondWrite := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var hello servicecontrol.UntrustedResponse
	s.requestLoop.exchange = func(ctx context.Context) error {
		call := calls.Add(1)
		if call > 2 {
			<-ctx.Done()
			return ctx.Err()
		}
		frame := dispatchHello
		if call == 2 {
			s.mu.Lock()
			acknowledged := s.auth.active != nil && s.auth.active.cleaned && s.auth.previousAcknowledgedLocked()
			s.mu.Unlock()
			if !acknowledged {
				t.Error("next request preceded original cleanup and owner acknowledgement")
			}
			frame = dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.Status)
		}
		return s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, frame), func(_ context.Context, raw []byte) error {
			reply, err := servicecontrol.DecodeUntrustedResponseFrame(raw)
			if err != nil {
				return err
			}
			if call == 1 {
				hello = reply
				close(firstWrite)
				<-release
			} else {
				if reply.Snapshot == nil || s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
					t.Error("status request created authority or lost response")
				}
				close(secondWrite)
			}
			return nil
		})
	}
	served := serveRuntimeLoopFixture(t, s)
	select {
	case <-firstWrite:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Serve did not authenticate its first request")
	}
	if calls.Load() != 1 {
		t.Error("request reception overlapped pending response")
	}
	close(release)
	select {
	case <-secondWrite:
	case err := <-served:
		t.Fatal("Serve stopped instead of receiving the next acknowledged request", err)
	case <-time.After(time.Second):
		t.Fatal("Serve did not receive its next request")
	}
}

func TestRuntimeServiceServeCallsUnconfiguredExchangeAndJoins(t *testing.T) {
	c := previewConfigFixture(t)
	c.Budgets.Cleanup, c.Budgets.Observe = 50*time.Millisecond, 10*time.Millisecond
	s, err := NewRuntimeService(c, RequestLoopBudgets{Wait: 20 * time.Millisecond, Request: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { closed.Add(1); return nil }}, nil
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	}
	defer cleanup()
	select {
	case err = <-served:
		if !errors.Is(err, ErrRuntimeUnconfirmed) {
			t.Fatal("runtime did not report its failed exchange", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Serve never called its configured runtime exchange")
	}
	if !s.Snapshot().CleanupConfirmed || !s.Snapshot().Stopping || closed.Load() != 1 {
		t.Fatal("runtime exchange failure escaped Service cleanup", s.Snapshot(), closed.Load())
	}
	if s.auth.active != nil || s.auth.attempts != 0 || s.Snapshot().Generation != 0 {
		t.Fatal("unconfigured runtime acquired a request or authority")
	}
}
