package servicehost

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceExchangeReservesOwnershipBeforeAcquisitionAndBurnsOnce(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil || r == nil {
		t.Fatal("exchange ownership not reserved", err)
	}
	if r.issuer != s.issuer || r.owner != s.authOwner || a.active != r {
		t.Fatal("reservation lost exact service owner")
	}
	if r.batch.collector != nil || a.attempts != 1 || len(a.seen) != 0 {
		t.Fatal("reservation acquired batch or spent wrong budget")
	}
	if next, err := a.reserveExchange(ctx); next != nil || !errors.Is(err, ErrProvisioningRejected) {
		t.Fatal("in-flight exchange admitted another acquisition", err)
	}
	if a.attempts != 1 {
		t.Fatal("rejected reservation consumed another attempt")
	}
	s.requestStop()
	if !s.issuer.Snapshot().Closed || r.worker.Err() == nil {
		t.Fatal("stop did not revoke before canceling reserved worker")
	}
	if next, err := a.reserveExchange(ctx); next != nil || err == nil {
		t.Fatal("stopped exchange reserved another acquisition")
	}
}

// This private seam models resource ownership only. It creates no native
// receipts, authenticated Read, input provenance, or next-request permit.
func TestServiceExchangeRetainsPartialEndpointUntilOriginalWorkerCleanupAcknowledges(t *testing.T) {
	s, a, serveDone := authTestService(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := &serviceready.Endpoint{}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var ack atomic.Bool
	cleanupAttempt := make(chan struct{}, 1)
	var wrongWorker atomic.Bool
	workerDone := make(chan error, 1)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ack.Store(true)
		cleanup, end := context.WithTimeout(context.Background(), time.Second)
		defer end()
		_ = s.Close(cleanup)
	})
	go func() {
		workerDone <- r.runOwner(func() error {
			owner := authFixtureGoroutine()
			_, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return serviceAuthEndpointOps{concrete: endpoint, close: func(context.Context) error {
					if authFixtureGoroutine() != owner {
						wrongWorker.Store(true)
					}
					select {
					case cleanupAttempt <- struct{}{}:
					default:
					}
					if !ack.Load() {
						return ErrRuntimeUnconfirmed
					}
					return nil
				}}, errors.New("partial endpoint constructor failure")
			})
			return err
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("endpoint constructor did not enter")
	}
	short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := s.Close(short); err == nil {
		t.Error("service finished before endpoint constructor returned")
	}
	end()
	releaseOnce.Do(func() { close(release) })
	select {
	case <-cleanupAttempt:
	case <-ctx.Done():
		t.Fatal("partial endpoint was not reclaimed")
	}
	s.mu.Lock()
	retained := r.endpoint != nil && r.endpoint.ops.concrete == endpoint && !r.cleaned && !r.finished
	s.mu.Unlock()
	if !retained {
		t.Fatal("partial endpoint ownership was discarded")
	}
	select {
	case <-r.ownerDone:
		t.Fatal("owner ended before endpoint cleanup acknowledgement")
	default:
	}
	if s.Snapshot().CleanupConfirmed {
		t.Fatal("unknown endpoint became service cleanup proof")
	}
	ack.Store(true)
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.Close(cleanup); err != nil {
		t.Fatal("same endpoint did not recover", err)
	}
	select {
	case err := <-workerDone:
		if err == nil {
			t.Fatal("partial acquisition failure became exchange success")
		}
	case <-cleanup.Done():
		t.Fatal("original worker did not end")
	}
	select {
	case <-serveDone:
	case <-cleanup.Done():
		t.Fatal("service did not join")
	}
	if wrongWorker.Load() {
		t.Fatal("endpoint cleanup moved to another worker")
	}
}

func TestServiceExchangeBatchCleanupPanicStillAttemptsEndpointAndKeepsWorker(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	batch := fixtureAuthBatch(a, dispatchHello)
	attempted := make(chan struct{}, 1)
	var batchCalls atomic.Int32
	batch.close = func(context.Context) error {
		if batchCalls.Add(1) == 1 {
			panic("private batch cleanup failure")
		}
		return nil
	}
	if err := r.attachBatchOps(batch); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- r.runOwner(func() error {
			_, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
				return serviceAuthEndpointOps{concrete: &serviceready.Endpoint{}, close: func(context.Context) error {
					select {
					case attempted <- struct{}{}:
					default:
					}
					return nil
				}}, nil
			})
			if err != nil {
				return err
			}
			return ErrProvisioningRejected
		})
	}()
	independent := true
	select {
	case <-attempted:
	case <-time.After(100 * time.Millisecond):
		independent = false
	}
	select {
	case <-r.ownerDone:
		t.Error("cleanup panic ended owner before both acknowledgements")
	default:
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.waitAuthRequest(cleanup); err != nil {
		t.Fatal("same-worker retry did not reclaim both owners", err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cleanup panic became exchange success")
		}
	case <-cleanup.Done():
		t.Fatal("original worker did not join")
	}
	if !independent {
		t.Fatal("batch cleanup panic skipped independent endpoint cleanup")
	}
}

func TestServiceExchangeCanceledLateBatchIsRetainedButNeverStarted(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var starts, closes int
	batch := fixtureAuthBatch(a, dispatchHello)
	batch.start = func(context.Context) error { starts++; return nil }
	batch.close = func(context.Context) error { closes++; return nil }
	foreign := batch
	foreign.owner = &serviceauthowner.Owner{}
	if err := r.attachBatchOps(foreign); err == nil || r.batch.collector != nil || len(a.seen) != 0 {
		t.Fatal("foreign owner was retained")
	}
	s.requestStop()
	if err := r.attachBatchOps(batch); err == nil {
		t.Fatal("late batch opened canceled exchange")
	}
	if r.batch.collector != batch.collector || len(a.seen) != 1 || a.attempts != 1 {
		t.Fatal("same-owner late constructor was discarded or spent another attempt")
	}
	if err := r.runOwner(func() error { return r.exchangeBody(func(context.Context, []byte) error { return nil }) }); err == nil {
		t.Fatal("canceled worker succeeded")
	}
	if starts != 0 || closes != 1 {
		t.Fatal("late batch started or was not reclaimed", starts, closes)
	}
	after := fixtureAuthBatch(a, dispatchHello)
	if err := r.attachBatchOps(after); err == nil || r.batch.collector != batch.collector || len(a.seen) != 1 {
		t.Fatal("ended worker accepted another owner")
	}
	if closes != 1 {
		t.Fatal("rejected owner was closed on caller's behalf")
	}
}

func TestServiceExchangeEmptyReservationStillRequiresWorkerAcknowledgement(t *testing.T) {
	s, a, _ := authTestService(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := s.Close(short); err == nil {
		t.Error("empty reservation was treated as worker acknowledgement")
	}
	end()
	select {
	case <-r.ownerDone:
		t.Error("worker acknowledgement synthesized without worker")
	default:
	}
	started := false
	if err := r.runOwner(func() error { started = true; return nil }); err == nil {
		t.Error("stopped placeholder started acquisition")
	}
	if started {
		t.Error("empty canceled worker acquired resources")
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.Close(cleanup); err != nil {
		t.Fatal(err)
	}
}

func TestServiceExchangeEndpointCleanupDoesNotMintTurnoverReadiness(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	a.limit = 2
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closes := 0
	if err := r.runOwner(func() error {
		_, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
			return serviceAuthEndpointOps{concrete: &serviceready.Endpoint{}, close: func(context.Context) error {
				closes++
				if s.issuer.Snapshot().Closed {
					t.Error("normal owned-resource cleanup revoked issuer")
				}
				return nil
			}}, nil
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if closes != 1 || !r.cleaned || !r.finished || r.endpoint.closed != nil {
		t.Fatal("memory cleanup created or lost acknowledgements")
	}
	select {
	case <-r.ownerDone:
	default:
		t.Fatal("worker end not acknowledged")
	}
	if next, err := a.reserveExchange(ctx); next != nil || err == nil {
		t.Fatal("cleanup alone minted next-request readiness")
	}
	if a.attempts != 1 || s.issuer.Snapshot().Generation != 0 {
		t.Fatal("blocked turnover consumed budget or created authority")
	}
}

func TestServiceExchangeFatalBatchCleanupRetiresWithoutEndpointRetry(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	batch := fixtureAuthBatch(a, dispatchHello)
	var batchCloses, endpointCloses atomic.Int32
	batch.close = func(context.Context) error { batchCloses.Add(1); return bootstrapauth.ErrFatal }
	if err := r.attachBatchOps(batch); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- r.runOwner(func() error {
			_, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
				return serviceAuthEndpointOps{concrete: &serviceready.Endpoint{}, close: func(context.Context) error { endpointCloses.Add(1); return nil }}, nil
			})
			if err != nil {
				return err
			}
			return ErrProvisioningRejected
		})
	}()
	select {
	case err := <-result:
		if !errors.Is(err, bootstrapauth.ErrFatal) {
			t.Fatal("fatal classification lost", err)
		}
	case <-ctx.Done():
		t.Fatal("fatal worker did not retire")
	}
	for range 2 {
		if err := s.waitAuthRequest(ctx); !errors.Is(err, bootstrapauth.ErrFatal) {
			t.Fatal("fatal owner produced cleanup success", err)
		}
	}
	if err := r.close(ctx); !errors.Is(err, bootstrapauth.ErrFatal) {
		t.Fatal("retired owner accepted another cleanup attempt", err)
	}
	if endpointCloses.Load() != 0 || batchCloses.Load() != 1 || r.endpoint == nil || r.cleaned {
		t.Fatal("fatal retried native cleanup or dropped endpoint ownership")
	}
	select {
	case <-r.done:
		t.Fatal("fatal worker minted cleanup acknowledgement")
	default:
	}
}

func TestServiceExchangeCleanupSharesOriginalDeadlineWithIndependentEndpoint(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	s.cfg.Budgets.Cleanup = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var batchDeadline, endpointDeadline time.Time
	endpointAttempt := make(chan struct{}, 1)
	batch := fixtureAuthBatch(a, dispatchHello)
	batch.close = func(c context.Context) error {
		if calls.Add(1) == 1 {
			batchDeadline, _ = c.Deadline()
			<-c.Done()
			return ErrRuntimeUnconfirmed
		}
		return nil
	}
	if err := r.attachBatchOps(batch); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- r.runOwner(func() error {
			_, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
				return serviceAuthEndpointOps{concrete: &serviceready.Endpoint{}, close: func(c context.Context) error {
					endpointDeadline, _ = c.Deadline()
					select {
					case endpointAttempt <- struct{}{}:
					default:
					}
					return nil
				}}, nil
			})
			if err != nil {
				return err
			}
			return ErrProvisioningRejected
		})
	}()
	select {
	case <-endpointAttempt:
	case <-ctx.Done():
		t.Fatal("unknown batch starved endpoint cleanup")
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.waitAuthRequest(cleanup); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
	case <-cleanup.Done():
		t.Fatal("worker did not join")
	}
	remaining := endpointDeadline.Sub(batchDeadline)
	if remaining <= 0 || remaining > 150*time.Millisecond {
		t.Fatal("cleanup did not split one original finite deadline", remaining)
	}
}

func TestServiceExchangeEndpointPanicRetainsOnlyItsUnconfirmedAcknowledgement(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	batch := fixtureAuthBatch(a, dispatchHello)
	var batchCalls, endpointCalls atomic.Int32
	endpointAttempt := make(chan struct{}, 1)
	batch.close = func(context.Context) error { batchCalls.Add(1); return nil }
	if err := r.attachBatchOps(batch); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- r.runOwner(func() error {
			_, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
				return serviceAuthEndpointOps{concrete: &serviceready.Endpoint{}, close: func(context.Context) error {
					if endpointCalls.Add(1) == 1 {
						endpointAttempt <- struct{}{}
						panic("private endpoint cleanup failure")
					}
					return nil
				}}, nil
			})
			if err != nil {
				return err
			}
			return ErrProvisioningRejected
		})
	}()
	select {
	case <-endpointAttempt:
	case <-ctx.Done():
		t.Fatal("endpoint cleanup did not enter")
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.waitAuthRequest(cleanup); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("endpoint panic became exchange success")
		}
	case <-cleanup.Done():
		t.Fatal("endpoint worker did not join")
	}
	if batchCalls.Load() != 1 || endpointCalls.Load() != 2 || !r.cleaned {
		t.Fatal("panic dropped an owner or repeated a confirmed batch cleanup")
	}
}
