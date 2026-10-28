package servicehost

import (
	"context"
	"errors"
	"fmt"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"strings"
	"testing"
	"time"
)

// このticketはmemoryで順序だけを検証する。正規認証・configDigest・policyを作らない。
func reservationTestIntent(s *Service, expires time.Time) *serviceOwnerIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent := &serviceOwnerIntent{owner: s, issuer: s.issuer, expires: expires}
	s.ownerIntent = intent
	return intent
}
func TestServiceReservationIntentIsBurnedBeforeReturningSingleReservation(t *testing.T) {
	s, _, _, _, _ := dispatchTestOwner(t, 4)
	intent := reservationTestIntent(s, time.Now().Add(time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reservation, err := s.reserveOwnerIntent(ctx, intent)
	if err != nil || reservation == nil {
		t.Fatal("sameowner reservation rejected")
	}
	if _, err := s.reserveOwnerIntent(ctx, intent); err == nil {
		t.Fatal("intent was not irrevocably burned before delivery")
	}
	if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
		t.Fatal("ordering reservation created authority")
	}
}

func TestServiceReservationOnlySameOwnerCanConsumeExactlyOnce(t *testing.T) {
	s, _, _, _, _ := dispatchTestOwner(t, 4)
	other, _, _, _, _ := dispatchTestOwner(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	intent := reservationTestIntent(s, time.Now().Add(time.Minute))
	r, err := s.reserveOwnerIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.consumeOwnerReservation(r); err == nil {
		t.Fatal("foreign Service consumed reservation")
	}
	work, err := s.consumeOwnerReservation(r)
	if err != nil || work.Context() == nil {
		t.Fatal("sameowner consume rejected")
	}
	if _, err := s.consumeOwnerReservation(r); err == nil {
		t.Fatal("reservation consumed more than once")
	}
	if err := s.finishOwnerReservation(work); err != nil {
		t.Fatal(err)
	}
}

func TestServiceReservationStopRevokesBeforeWorkerCancellationAndNeverUnburns(t *testing.T) {
	s, _, _, _, _ := dispatchTestOwner(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	intent := reservationTestIntent(s, time.Now().Add(time.Minute))
	r, err := s.reserveOwnerIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.consumeOwnerReservation(r)
	if err != nil {
		t.Fatal(err)
	}
	s.stopFor(nil)
	select {
	case <-work.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel the owned reservation worker")
	}
	if !r.issuer.Snapshot().Closed {
		t.Fatal("worker canceled before same issuer was revoked")
	}
	if _, err := s.reserveOwnerIntent(ctx, intent); err == nil {
		t.Fatal("stop/cancellation restored burned intent")
	}
	if _, err := s.consumeOwnerReservation(r); err == nil {
		t.Fatal("stopped reservation consumed again")
	}
	if err := s.finishOwnerReservation(work); err != nil {
		t.Fatal(err)
	}
}

func TestServiceReservationCancellationCannotCompleteServiceBeforeWorkerAck(t *testing.T) {
	s, _, _, done, _ := dispatchTestOwner(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
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
	stop, finish := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err = s.Close(stop)
	finish()
	if err == nil {
		t.Fatal("reservation cancellation was mistaken for joined worker completion")
	}
	snapshot := s.Snapshot()
	if snapshot.State != StopPending || snapshot.CleanupConfirmed {
		t.Fatal("unacknowledged reservation allowed SCM cleanup proof")
	}
	select {
	case <-done:
		t.Fatal("Serve returned with an unacknowledged worker")
	default:
	}
	if err := s.finishOwnerReservation(work); err != nil {
		t.Fatal(err)
	}
	retry, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	if err := s.Close(retry); err != nil {
		t.Fatal(err)
	}
	if !s.Snapshot().CleanupConfirmed {
		t.Fatal("sameowner worker ack did not unblock cleanup")
	}
}

func TestServiceReservationStopBeforeAdmissionAndLateUndeliveredWorkerAreDenied(t *testing.T) {
	for _, reserveFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(reserveFirst), func(t *testing.T) {
			s, _, _, _, _ := dispatchTestOwner(t, 4)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			intent := reservationTestIntent(s, time.Now().Add(time.Minute))
			var r *serviceReservation
			if reserveFirst {
				var err error
				r, err = s.reserveOwnerIntent(ctx, intent)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := s.reserveOwnerIntent(ctx, intent); err == nil {
				t.Fatal("stop admitted a new reservation")
			}
			if reserveFirst {
				if _, err := s.consumeOwnerReservation(r); err == nil {
					t.Fatal("late delivery escaped owner abandonment")
				}
			}
			if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority || !s.Snapshot().CleanupConfirmed {
				t.Fatal("ordering tickets changed authority or lost cleanup")
			}
		})
	}
}
func TestServiceReservationCanceledDeliveryNeverUnburnsOrCreatesAWorker(t *testing.T) {
	s, _, _, _, _ := dispatchTestOwner(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	intent := reservationTestIntent(s, time.Now().Add(time.Minute))
	r, err := s.reserveOwnerIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := s.consumeOwnerReservation(r); err == nil {
		t.Fatal("canceled delivery created a worker ticket")
	}
	fresh, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if _, err := s.reserveOwnerIntent(fresh, intent); err == nil {
		t.Fatal("delivery cancellation restored intent")
	}
	if err := s.Close(fresh); err != nil {
		t.Fatal("unclaimed canceled reservation could not be abandoned")
	}
}
func TestServiceReservationOnlySameOwnerAckCanFinishAndIsSingleUse(t *testing.T) {
	s, _, _, _, _ := dispatchTestOwner(t, 4)
	other, _, _, _, _ := dispatchTestOwner(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
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
	if err := other.finishOwnerReservation(work); err == nil {
		t.Fatal("foreign owner acknowledged worker completion")
	}
	if err := s.finishOwnerReservation(work); err != nil {
		t.Fatal(err)
	}
	if err := s.finishOwnerReservation(work); err == nil {
		t.Fatal("worker ack replay accepted")
	}
}
func TestServiceReservationConcurrentStopAndAdmissionHasOneOwnerOrder(t *testing.T) {
	for n := 0; n < 8; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, _, _, _, _ := dispatchTestOwner(t, 4)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			intent := reservationTestIntent(s, time.Now().Add(time.Minute))
			start := make(chan struct{})
			reserved := make(chan *serviceReservation, 1)
			stopped := make(chan struct{})
			go func() { <-start; r, _ := s.reserveOwnerIntent(ctx, intent); reserved <- r }()
			go func() { <-start; s.stopFor(nil); close(stopped) }()
			close(start)
			r := <-reserved
			<-stopped
			if r != nil {
				if _, err := s.consumeOwnerReservation(r); err == nil {
					t.Fatal("admission raced past stop")
				}
			}
			if _, err := s.reserveOwnerIntent(ctx, intent); err == nil {
				t.Fatal("stopped intent reused")
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if !s.Snapshot().CleanupConfirmed || s.Snapshot().Generation != 0 {
				t.Fatal("raced reservation lost owner or created grant")
			}
		})
	}
}

func TestServiceReservationCannotBeProjectedAsEmptyWireStatus(t *testing.T) {
	s, d, _, _, _ := dispatchTestOwner(t, 4)
	hello := dispatchTestFrame(t, d, dispatchHello)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.reserveOwnerIntent(ctx, reservationTestIntent(s, time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	response := dispatchTestFrame(t, d, dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.Status))
	if response.Snapshot != nil || response.Error == nil || response.Error.Code != "SERVICE_NOT_READY" {
		t.Fatal("unprojected reservation became empty authoritative-looking status")
	}
}

func TestServiceReservationFatalReturnStillWaitsForClaimedWorkerAck(t *testing.T) {
	planeClosed := make(chan struct{}, 1)
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }, close: func(context.Context) error { planeClosed <- struct{}{}; return nil }}
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: 20 * time.Millisecond, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	v := &dispatchTestVerifier{}
	d, err := newServiceDispatcher(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: 4}, v)
	if err != nil {
		t.Fatal(err)
	}
	v.owner = d
	states := make(chan ServiceState, 16)
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), nil, states) }()
	wait, stopWait := context.WithTimeout(context.Background(), time.Second)
	defer stopWait()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if s.Snapshot().State == Running {
			break
		}
		select {
		case <-tick.C:
		case <-wait.Done():
			t.Fatal("owner not ready")
		}
	}
	r, err := s.reserveOwnerIntent(wait, reservationTestIntent(s, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.consumeOwnerReservation(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.finishOwnerReservation(work) })
	// 分類だけを注入する。実impersonation/token操作はしない。
	s.stopFor(bootstrapauth.ErrFatal)
	select {
	case <-work.Context().Done():
	case <-wait.Done():
		t.Fatal("worker cancellation missing")
	}
	select {
	case <-done:
		t.Fatal("fatal path published ServeDone before worker ack")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-planeClosed:
		t.Fatal("fatal reservation wait resumed native plane cleanup")
	default:
	}
	if s.Snapshot().CleanupConfirmed {
		t.Fatal("fatal path invented cleanup proof")
	}
	if err := s.finishOwnerReservation(work); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, bootstrapauth.ErrFatal) {
			t.Fatal("fatal classification lost")
		}
	case <-wait.Done():
		t.Fatal("ack did not release pure return wait")
	}
}
