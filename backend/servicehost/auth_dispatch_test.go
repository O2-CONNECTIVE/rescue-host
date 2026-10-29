package servicehost

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func authTestService(t *testing.T, limit uint64) (*Service, *serviceAuthAdapter, <-chan error) {
	t.Helper()
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) {
		return &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := newServiceAuthAdapter(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: 4}, limit)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	states := make(chan ServiceState, 32)
	go func() { done <- s.Serve(context.Background(), nil, states) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for s.Snapshot().State != Running {
		select {
		case <-deadline.C:
			t.Fatal("service not ready")
		case <-time.After(time.Millisecond):
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, a, done
}

func TestServiceAuthStopWaitsForLateStartOwnerAcknowledgement(t *testing.T) {
	s, a, serveDone := authTestService(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var collects, closed atomic.Int32
	batch := serviceAuthBatchOps{collector: &operatorauth.Collector{},
		start: func(context.Context) error { close(entered); <-release; return nil },
		collect: func(context.Context) (*operatorauth.Read, error) {
			collects.Add(1)
			return nil, errors.New("late collect")
		},
		consume: func(context.Context, *operatorauth.Read) ([]byte, error) {
			return nil, errors.New("unexpected consume")
		},
		close: func(context.Context) error { closed.Add(1); return nil },
	}
	exchanged := make(chan error, 1)
	go func() { exchanged <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { return nil }) }()
	<-entered
	defer func() { close(release); <-exchanged }()
	cleanup, end := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err := s.Close(cleanup)
	end()
	if err == nil {
		t.Fatal("service confirmed cleanup before auth Start returned")
	}
	select {
	case <-serveDone:
		t.Fatal("service returned before auth owner ack")
	default:
	}
	if !s.Snapshot().Stopping {
		t.Fatal("stop not published")
	}
}

func TestServiceAuthConnectsSameOwnerProofToFiniteDispatch(t *testing.T) {
	_, a, _ := authTestService(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var order []string
	proof := &operatorauth.Read{}
	collector := &operatorauth.Collector{}
	batch := serviceAuthBatchOps{collector: collector,
		start:   func(context.Context) error { order = append(order, "start"); return nil },
		collect: func(context.Context) (*operatorauth.Read, error) { order = append(order, "collect"); return proof, nil },
		consume: func(_ context.Context, got *operatorauth.Read) ([]byte, error) {
			if got != proof {
				t.Fatal("foreign proof supplied")
			}
			order = append(order, "consume")
			return []byte(dispatchHello), nil
		},
		close: func(context.Context) error { t.Fatal("normal helper completion was revoked"); return nil },
	}
	err := a.exchangeOps(ctx, batch, func(writeCtx context.Context, frame []byte) error {
		if !controlBudget(writeCtx) {
			t.Fatal("writer lost finite request")
		}
		reply, err := servicecontrol.DecodeUntrustedResponseFrame(frame)
		if err != nil || reply.Type != "hello" {
			t.Fatalf("reply: %+v, %v", reply, err)
		}
		order = append(order, "write")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "start,collect,consume,write" {
		t.Fatal(order)
	}
}

func fixtureAuthBatch(frame string) serviceAuthBatchOps {
	proof := &operatorauth.Read{}
	return serviceAuthBatchOps{collector: &operatorauth.Collector{},
		start:   func(context.Context) error { return nil },
		collect: func(context.Context) (*operatorauth.Read, error) { return proof, nil },
		consume: func(context.Context, *operatorauth.Read) ([]byte, error) { return []byte(frame), nil },
		close:   func(context.Context) error { return nil },
	}
}

func TestServiceAuthCopiedAdapterCannotCreateIndependentAdmission(t *testing.T) {
	_, a, _ := authTestService(t, 1)
	copyAdapter := *a
	var starts atomic.Int32
	batch := fixtureAuthBatch(dispatchHello)
	batch.start = func(context.Context) error { starts.Add(1); return nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := copyAdapter.exchangeOps(ctx, batch, func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("copied adapter admitted request")
	}
	if starts.Load() != 0 {
		t.Fatal("foreign adapter started native owner")
	}
}

type authDelayedDeadline struct {
	context.Context
	deadline time.Time
}

func (c authDelayedDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestServiceAuthElapsedDeadlineCannotWaitForTimerBeforeRejection(t *testing.T) {
	_, a, _ := authTestService(t, 1)
	var starts atomic.Int32
	batch := fixtureAuthBatch(dispatchHello)
	batch.start = func(context.Context) error { starts.Add(1); return nil }
	ctx := authDelayedDeadline{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	if err := a.exchangeOps(ctx, batch, func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("elapsed deadline admitted")
	}
	if starts.Load() != 0 {
		t.Fatal("elapsed deadline started owner before timer signal")
	}
}

func TestServiceAuthConsumeDoesNotRunUnderServiceLock(t *testing.T) {
	s, a, _ := authTestService(t, 1)
	batch := fixtureAuthBatch(dispatchHello)
	unlocked := false
	batch.consume = func(context.Context, *operatorauth.Read) ([]byte, error) {
		unlocked = s.mu.TryLock()
		if unlocked {
			s.mu.Unlock()
		}
		return nil, errors.New("fixture consume rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.exchangeOps(ctx, batch, func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("consume failure accepted")
	}
	if !unlocked {
		t.Fatal("consume ran under Service lock")
	}
}

func TestServiceAuthNativeProviderAndZeroBudgetStayUnconfigured(t *testing.T) {
	if _, err := nativeServiceAuthBatch(nil); err == nil {
		t.Fatal("nil batch accepted")
	}
	if _, err := nativeServiceAuthBatch(&operatorauth.Batch{}); err == nil {
		t.Fatal("unprepared batch accepted")
	}
	if _, err := newServiceAuthAdapter(&Service{}, serviceDispatchProfile{}, 0); err == nil {
		t.Fatal("default launch budget enabled")
	}
}

func TestServiceAuthCumulativeLaunchBudgetAndCollectorAreNotReusable(t *testing.T) {
	for _, limit := range []uint64{1, 2} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			_, a, _ := authTestService(t, limit)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			first := fixtureAuthBatch(dispatchHello)
			if err := a.exchangeOps(ctx, first, func(context.Context, []byte) error { return nil }); err != nil {
				t.Fatal(err)
			}
			var starts atomic.Int32
			next := fixtureAuthBatch(dispatchHello)
			if limit == 2 {
				next.collector = first.collector
			}
			next.start = func(context.Context) error { starts.Add(1); return nil }
			if err := a.exchangeOps(ctx, next, func(context.Context, []byte) error { return nil }); err == nil {
				t.Fatal("reused collector or exhausted budget accepted")
			}
			if starts.Load() != 0 {
				t.Fatal("rejected request started")
			}
			a.service.mu.Lock()
			attempts := a.attempts
			a.service.mu.Unlock()
			if attempts != 1 {
				t.Fatal("rejected admission consumed new authority")
			}
		})
	}
}

func TestServiceAuthFailureRevokesSameIssuerAndClosesOutsideLock(t *testing.T) {
	for _, stage := range []string{"start", "collect", "consume", "write", "panic"} {
		t.Run(stage, func(t *testing.T) {
			s, a, _ := authTestService(t, 1)
			s.mu.Lock()
			issuer := s.issuer
			s.mu.Unlock()
			batch := fixtureAuthBatch(dispatchHello)
			var closes atomic.Int32
			batch.close = func(context.Context) error {
				if !issuer.Snapshot().Closed {
					t.Error("owner cleanup preceded same issuer revoke")
				}
				if !s.mu.TryLock() {
					t.Error("close callback under service lock")
				} else {
					s.mu.Unlock()
				}
				closes.Add(1)
				return nil
			}
			fail := errors.New("fixture stage rejected")
			switch stage {
			case "start":
				batch.start = func(context.Context) error { return fail }
			case "collect":
				batch.collect = func(context.Context) (*operatorauth.Read, error) { return nil, fail }
			case "consume":
				batch.consume = func(context.Context, *operatorauth.Read) ([]byte, error) { return nil, fail }
			case "panic":
				batch.consume = func(context.Context, *operatorauth.Read) ([]byte, error) { panic("fixture panic") }
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := a.exchangeOps(ctx, batch, func(context.Context, []byte) error {
				if stage == "write" {
					return fail
				}
				return nil
			})
			if err == nil {
				t.Fatal("failure reported success")
			}
			if !issuer.Snapshot().Closed || closes.Load() == 0 {
				t.Fatal("failure lost same issuer/cleanup")
			}
		})
	}
}

func TestServiceAuthUnconfirmedCleanupRetainsOwnerUntilAcknowledged(t *testing.T) {
	s, a, serveDone := authTestService(t, 1)
	batch := fixtureAuthBatch(dispatchHello)
	var owner uint64
	batch.start = func(context.Context) error { owner = authFixtureGoroutine(); return errors.New("start rejected") }
	var ack atomic.Bool
	attempted := make(chan struct{}, 1)
	var wrong atomic.Bool
	batch.close = func(context.Context) error {
		if authFixtureGoroutine() != owner {
			wrong.Store(true)
		}
		select {
		case attempted <- struct{}{}:
		default:
		}
		if !ack.Load() {
			return ErrRuntimeUnconfirmed
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	exchanged := make(chan error, 1)
	go func() { exchanged <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { return nil }) }()
	<-attempted
	select {
	case <-exchanged:
		t.Error("original owner returned before cleanup acknowledgement")
	default:
	}
	s.mu.Lock()
	r := a.active
	held := r != nil && !r.finished && !r.cleaned
	s.mu.Unlock()
	if !held {
		t.Fatal("unconfirmed owner dropped")
	}
	select {
	case <-serveDone:
		t.Fatal("service returned before owner acknowledged")
	default:
	}
	ack.Store(true)
	closeCtx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exchanged:
	case <-closeCtx.Done():
		t.Fatal("owner failed to leave after acknowledged cleanup")
	}
	if wrong.Load() {
		t.Fatal("cleanup moved to another goroutine")
	}
}

// Memory-only observation of the retained execution worker, never authentication.
func authFixtureGoroutine() uint64 {
	var b [80]byte
	n := runtime.Stack(b[:], false)
	var id uint64
	_, _ = fmt.Sscanf(string(b[:n]), "goroutine %d ", &id)
	return id
}

func TestServiceAuthResponseWriteRemainsAnOwnedOperation(t *testing.T) {
	s, a, serveDone := authTestService(t, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		result <- a.exchangeOps(ctx, fixtureAuthBatch(dispatchHello), func(context.Context, []byte) error { close(entered); <-release; return errors.New("cancelled write") })
	}()
	<-entered
	defer func() { close(release); <-result }()
	closeCtx, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer end()
	if err := s.Close(closeCtx); err == nil {
		t.Fatal("unacknowledged writer reported reclaimed")
	}
	select {
	case <-serveDone:
		t.Fatal("writer ownership lost")
	default:
	}
}

func TestServiceAuthElapsedWriterDeadlineDoesNotConfirmCompletion(t *testing.T) {
	s, a, _ := authTestService(t, 1)
	deadline := time.Now().Add(25 * time.Millisecond)
	ctx := authDelayedDeadline{Context: context.Background(), deadline: deadline}
	err := a.exchangeOps(ctx, fixtureAuthBatch(dispatchHello), func(context.Context, []byte) error { <-time.After(time.Until(deadline) + time.Millisecond); return nil })
	if err == nil {
		t.Fatal("writer completion after actual deadline accepted")
	}
	if !s.Snapshot().Stopping {
		t.Fatal("elapsed writer did not revoke owner")
	}
}

func TestServiceAuthFrameAloneCannotMintAuthenticatedProof(t *testing.T) {
	_, a, _ := authTestService(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := a.verify(ctx, []byte(dispatchHello)); err == nil {
		t.Fatal("unowned frame authenticated")
	}
	if err := a.exchangeOps(ctx, fixtureAuthBatch(dispatchHello), func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.verify(ctx, []byte(dispatchHello)); err == nil {
		t.Fatal("completed proof reused")
	}
}

func TestServiceAuthQuiesceWritesReceiptBeforeOperationAcknowledgement(t *testing.T) {
	s, a, _ := authTestService(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var hello servicecontrol.UntrustedResponse
	if err := a.exchangeOps(ctx, fixtureAuthBatch(dispatchHello), func(_ context.Context, frame []byte) error {
		var err error
		hello, err = servicecontrol.DecodeUntrustedResponseFrame(frame)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	frame := dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.RevokeAllAndQuiesce)
	err := a.exchangeOps(ctx, fixtureAuthBatch(frame), func(writeCtx context.Context, frame []byte) error {
		if writeCtx.Err() != nil {
			t.Fatal("quiesce cancelled its own finite response operation")
		}
		response, err := servicecontrol.DecodeUntrustedResponseFrame(frame)
		if err != nil {
			return err
		}
		if response.Quiesce == nil || !response.Quiesce.RevocationApplied || response.Quiesce.WorkloadCleanupConfirmed {
			t.Fatal("receipt invented workload cleanup")
		}
		s.mu.Lock()
		issuer := s.issuer
		finished := a.active.finished
		s.mu.Unlock()
		if !issuer.Snapshot().Closed || finished {
			t.Fatal("receipt not same live owned operation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func authUnstartedMemoryOwner(t *testing.T) (*Service, *serviceAuthAdapter) {
	t.Helper()
	s, err := newService(ServiceConfig{Provisioning: lifetimePolicySource(func(context.Context) (ephemeral.ApprovedPolicy, error) {
		return ephemeral.ApprovedPolicy{}, ErrProvisioningUnconfigured
	}), Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return nil, ErrProvisioningUnconfigured })
	if err != nil {
		t.Fatal(err)
	}
	a, err := newServiceAuthAdapter(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: 4}, 1)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.issuer = issuer
	s.state = Running
	s.used = true
	s.started = true
	s.mu.Unlock()
	// No real Service worker, native object, or process exists in this fixture.
	return s, a
}

func TestServiceAuthFatalSentinelsRemainStickyWithoutClaimingCleanup(t *testing.T) {
	for _, sentinel := range []error{operatorauth.ErrFatal, bootstrapauth.ErrFatal} {
		for _, stage := range []string{"start", "collect", "close"} {
			t.Run(sentinel.Error()+"/"+stage, func(t *testing.T) {
				s, a := authUnstartedMemoryOwner(t)
				batch := fixtureAuthBatch(dispatchHello)
				nativeErr := errors.Join(sentinel, errors.New("private-native-error-text"))
				var closes int
				batch.close = func(context.Context) error {
					closes++
					if stage == "close" {
						return nativeErr
					}
					return nil
				}
				switch stage {
				case "start":
					batch.start = func(context.Context) error { return nativeErr }
				case "collect":
					batch.collect = func(context.Context) (*operatorauth.Read, error) { return nil, nativeErr }
				case "close":
					batch.start = func(context.Context) error { return ErrProvisioningRejected }
				}
				result := make(chan error, 1)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				go func() { result <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { return nil }) }()
				var err error
				select {
				case err = <-result:
				case <-ctx.Done():
					t.Fatal("fatal caller did not retire")
				}
				if !errors.Is(err, sentinel) {
					t.Fatalf("fatal lost: %v", err)
				}
				if strings.Contains(err.Error(), "private-native-error-text") {
					t.Fatal("native error text escaped")
				}
				s.mu.Lock()
				fatal := s.stopCause
				r := a.active
				cleaned := r.cleaned
				s.mu.Unlock()
				if !errors.Is(fatal, sentinel) || !fatalServiceThread(fatal) {
					t.Fatal("Service lost sticky fatal")
				}
				if cleaned {
					t.Fatal("fatal fabricated cleanup acknowledgement")
				}
				cleanup, end := context.WithTimeout(context.Background(), 2*time.Millisecond)
				closeErr := s.Close(cleanup)
				end()
				if !errors.Is(closeErr, sentinel) {
					t.Fatal("Close discarded sticky fatal", closeErr)
				}
				cleanup, end = context.WithTimeout(context.Background(), time.Second)
				waitErr := s.waitAuthRequest(cleanup)
				end()
				if !errors.Is(waitErr, sentinel) {
					t.Fatal("cleanup request did not preserve retired worker fatal", waitErr)
				}

				select {
				case <-r.done:
					t.Fatal("fatal closed cleanup ack")
				default:
				}
				select {
				case <-s.done:
					t.Fatal("fatal reported Service done")
				default:
				}
				want := 0
				if stage == "close" {
					want = 1
				}
				if closes != want {
					t.Fatal("fatal thread performed native retry")
				}
			})
		}
	}
}

func TestServiceAuthNewAdmissionWaitsForBothCleanupAndWorkerAcknowledgement(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	a.limit = 2
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.register(ctx, fixtureAuthBatch(dispatchHello))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	r.finished = true
	r.cleaned = true
	close(r.done)
	s.mu.Unlock()
	if _, err := a.register(ctx, fixtureAuthBatch(dispatchHello)); err == nil {
		t.Fatal("new admission displaced owner before worker acknowledgement")
	}
	close(r.ownerDone)
	if _, err := a.register(ctx, fixtureAuthBatch(dispatchHello)); err != nil {
		t.Fatal("acknowledged owner kept channel blocked")
	}
}

func TestServiceAuthWriterFatalSurvivesSimultaneousBudgetFailure(t *testing.T) {
	for _, sentinel := range []error{operatorauth.ErrFatal, bootstrapauth.ErrFatal} {
		for _, budget := range []string{"cancel", "deadline"} {
			t.Run(sentinel.Error()+"/"+budget, func(t *testing.T) {
				s, a := authUnstartedMemoryOwner(t)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				var request context.Context = ctx
				deadline := time.Now().Add(25 * time.Millisecond)
				if budget == "deadline" {
					request = authDelayedDeadline{Context: context.Background(), deadline: deadline}
				}
				batch := fixtureAuthBatch(dispatchHello)
				var closes atomic.Int32
				batch.close = func(context.Context) error { closes.Add(1); return nil }
				result := make(chan error, 1)
				go func() {
					result <- a.exchangeOps(request, batch, func(context.Context, []byte) error {
						if budget == "cancel" {
							cancel()
						} else {
							<-time.After(time.Until(deadline) + time.Millisecond)
						}
						return errors.Join(sentinel, errors.New("private-writer-error-text"))
					})
				}()
				var err error
				select {
				case err = <-result:
				case <-time.After(time.Second):
					t.Fatal("fatal writer failed to retire")
				}
				if !errors.Is(err, sentinel) {
					t.Fatal("budget failure overwrote writer fatal", err)
				}
				if strings.Contains(err.Error(), "private-writer-error-text") {
					t.Fatal("writer detail escaped")
				}
				if closes.Load() != 0 {
					t.Fatal("fatal writer performed native cleanup")
				}
				s.mu.Lock()
				r := a.active
				cause := s.stopCause
				cleaned, finished := r.cleaned, r.finished
				s.mu.Unlock()
				if !errors.Is(cause, sentinel) {
					t.Fatal("Service lost writer fatal")
				}
				if cleaned || finished {
					t.Fatal("fatal writer fabricated resource acknowledgement")
				}
				select {
				case <-r.done:
					t.Fatal("unconfirmed resources reported done")
				default:
				}
				select {
				case <-r.ownerDone:
				default:
					t.Fatal("fatal worker retirement not acknowledged")
				}
			})
		}
	}
}
