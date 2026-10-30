package servicehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

type fixedRuntimeAcceptanceOwner struct {
	runtimeGuestOwnerFixture
	finalized atomic.Int32
}

func (o *fixedRuntimeAcceptanceOwner) Finalize(_ context.Context, proof InnerCleanupProof) error {
	if !proof.Confirms(o.service) {
		return ErrRuntimeUnconfirmed
	}
	o.finalized.Add(1)
	return nil
}

type fixedRuntimeAcceptanceRun struct {
	service  *Service
	owner    *fixedRuntimeAcceptanceOwner
	target   *runtimeGuestTargetFixture
	controls chan Control
	done     chan struct{}
	specific bool
	code     uint32
}

// The binder, SCM owner, SID-bound dispatch, issuer and continuation are real.
// Existing private auth/inspection/child operations remain explicit memory fakes;
// this fixture supplies no Windows authentication or fixed-command exit evidence.
func fixedRuntimeAcceptanceFixture(t *testing.T) *fixedRuntimeAcceptanceRun {
	t.Helper()
	c := previewConfigFixture(t)
	c.RequestLimit, c.LaunchLimit = 8, 9
	c.Budgets.Cleanup, c.Budgets.Observe = 50*time.Millisecond, 10*time.Millisecond
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	o := &fixedRuntimeAcceptanceOwner{runtimeGuestOwnerFixture: runtimeGuestOwnerFixture{service: s}}
	if err = s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if err = s.BindRuntimeGuestEvaluationOwner(o); err != nil {
		t.Fatal(err)
	}
	if err = s.BindOperatorSIDPolicy(o, operatorauth.SIDPolicy{OperatorSID: c.Ready.Profile.Keys.OperatorSID, GuestSID: c.Ready.Profile.Keys.GuestSID}); err != nil {
		t.Fatal(err)
	}
	target := &runtimeGuestTargetFixture{sid: "S-1-5-21-7-8-9-2048", protected: true}
	lease := &serviceready.InspectionLease{}
	s.preview.runtimeGuest.kernel = runtimeGuestKernelFixture{lease: lease, target: target}
	o.prepare = func(ctx context.Context, ticket RuntimeGuestTicket, got *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
		if got != lease {
			return nil, childjob.GuestTokenExpectation{}, ErrProvisioningRejected
		}
		d, err := ticket.DescriptorFor(ctx, o)
		if err == nil {
			err = ticket.BindCreatedGuest(ctx, o, d.Namespace, target.sid)
		}
		return &childjob.Prepared{}, childjob.GuestTokenExpectation{}, err
	}
	s.callerGuard = func(context.Context) error { return nil }
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	f := &fixedRuntimeAcceptanceRun{service: s, owner: o, target: target, controls: make(chan Control, 1), done: make(chan struct{})}
	go func() {
		defer close(f.done)
		f.specific, f.code = serveReadySCM(s, f.controls, make(chan ServiceState, 64), func(c Control) Control { return c }, func(s ServiceState) ServiceState { return s })
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			t.Error("fixed evaluation SCM owner did not join")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for s.Snapshot().State != Running {
		select {
		case <-f.done:
			t.Fatal("fixed evaluation SCM owner stopped before readiness")
		case <-ctx.Done():
			t.Fatal("fixed evaluation SCM owner did not start")
		case <-time.After(time.Millisecond):
		}
	}
	return f
}

func TestFixedRuntimeAcceptanceRejectsDuplicateAndFailedResponse(t *testing.T) {
	for _, mode := range []string{"duplicate-confirm", "response-write-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := fixedRuntimeAcceptanceFixture(t)
			s := f.service
			hello := sidExchange(t, s.auth, dispatchHello, 7)
			intent := sidExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent), 7)
			batch, _ := sidBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"), 7)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			writes := 0
			err := s.auth.exchangeOps(ctx, batch, func(_ context.Context, raw []byte) error {
				writes++
				reply, err := servicecontrol.DecodeUntrustedResponseFrame(raw)
				if err != nil || reply.Approval == nil || f.target.commits.Load() != 0 {
					t.Error("confirmation response skipped approval or followed execution", err)
				}
				if mode == "response-write-failure" {
					return errors.New("synthetic response failure")
				}
				return nil
			})
			wantCommits := int32(0)
			if mode == "duplicate-confirm" {
				wantCommits = 1
				if err != nil || !s.issuer.Snapshot().Consumed {
					t.Fatal("authenticated confirmation did not continue", err)
				}
				replay, _ := sidBatch(s.auth, confirmationFrame(hello, intent.Intent, 3, "44444444444444444444444444444444"), 7)
				err = s.auth.exchangeOps(ctx, replay, func(context.Context, []byte) error { writes++; return nil })
			}
			if err == nil || writes != 1 || f.owner.prepares.Load() != 1 || f.target.inspections.Load() != 1 || f.target.commits.Load() != wantCommits {
				t.Fatal("failed response or duplicate confirmation repeated execution", err, writes, f.target.commits.Load())
			}
			if actual := s.issuer.Snapshot(); !actual.Closed || actual.HasAuthority || actual.Generation != 1 {
				t.Fatal("failure left canonical authority open", actual)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if f.owner.closes.Load() != 1 || f.target.closes.Load() != 1 || f.owner.finalized.Load() != 1 || !f.target.revoked.Load() {
				t.Fatal("failed request lost original cleanup ownership")
			}
		})
	}
}

func TestFixedRuntimeAcceptanceStopJoinsDelayedPreparationAndCommit(t *testing.T) {
	for _, phase := range []string{"prepare", "commit"} {
		t.Run(phase, func(t *testing.T) {
			f := fixedRuntimeAcceptanceFixture(t)
			s := f.service
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			entered, release := make(chan context.Context, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			defer unblock()
			wait := func(call context.Context) {
				entered <- call
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			partial := &childjob.Prepared{}
			if phase == "prepare" {
				f.owner.prepare = func(call context.Context, ticket RuntimeGuestTicket, _ *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
					d, err := ticket.DescriptorFor(call, f.owner)
					if err != nil {
						return nil, childjob.GuestTokenExpectation{}, err
					}
					wait(call)
					err = ticket.BindCreatedGuest(call, f.owner, d.Namespace, f.target.sid)
					return partial, childjob.GuestTokenExpectation{}, err
				}
			} else {
				f.target.onCommit = func(call context.Context) error { wait(call); return nil }
			}
			hello := sidExchange(t, s.auth, dispatchHello, 7)
			intent := sidExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent), 7)
			batch, _ := sidBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"), 7)
			var writes atomic.Int32
			result := make(chan error, 1)
			go func() {
				result <- s.auth.exchangeOps(ctx, batch, func(context.Context, []byte) error { writes.Add(1); return nil })
			}()
			var operation context.Context
			select {
			case operation = <-entered:
			case <-ctx.Done():
				t.Fatal("confirmation did not reach the delayed phase")
			}
			f.controls <- Stop
			select {
			case <-operation.Done():
			case <-ctx.Done():
				t.Fatal("SCM stop did not cancel the original operation")
			}
			if actual := s.issuer.Snapshot(); !actual.Closed || actual.HasAuthority {
				t.Error("operation cancellation preceded canonical revocation", actual)
			}
			short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if err := s.Close(short); err == nil || s.Snapshot().CleanupConfirmed || f.owner.finalized.Load() != 0 || f.target.closes.Load() != 0 {
				t.Error("held operation became cleanup acknowledgement", err)
			}
			end()
			select {
			case <-f.done:
				t.Error("SCM owner returned before delayed operation")
			default:
			}
			unblock()
			select {
			case err := <-result:
				if err == nil {
					t.Error("late success survived stop")
				}
			case <-ctx.Done():
				t.Fatal("original auth worker did not join")
			}
			if phase == "prepare" {
				material := s.preview.runtimeGuest.material.Load()
				if material == nil || material.prepared != partial || s.issuer.Snapshot().Generation != 0 || writes.Load() != 0 || f.target.inspections.Load() != 0 || f.target.commits.Load() != 0 {
					t.Error("late preparation lost its partial or reached approval/execution")
				}
			} else if writes.Load() != 1 || f.target.commits.Load() != 1 {
				t.Error("commit did not follow exactly one completed response")
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if f.owner.closes.Load() != 1 || f.target.closes.Load() != 1 || f.owner.finalized.Load() != 1 || !f.target.revoked.Load() {
				t.Fatal("stopped operation lost original cleanup ownership")
			}
		})
	}
}

func TestFixedRuntimeAcceptanceSCMRetainsOwnerAcrossCleanupRetry(t *testing.T) {
	f := fixedRuntimeAcceptanceFixture(t)
	s := f.service
	var recoverable atomic.Bool
	t.Cleanup(func() { recoverable.Store(true) })
	failed := make(chan struct{}, 1)
	var original RuntimeGuestTicket
	var originalLease *serviceready.InspectionLease
	f.owner.closeGuest = func(_ context.Context, ticket RuntimeGuestTicket, lease *serviceready.InspectionLease) error {
		if original == (RuntimeGuestTicket{}) {
			original, originalLease = ticket, lease
		}
		if ticket != original || lease != originalLease || !s.issuer.Snapshot().Closed {
			t.Error("cleanup changed owner identity or preceded revocation")
		}
		if !recoverable.Load() {
			select {
			case failed <- struct{}{}:
			default:
			}
			return ErrRuntimeUnconfirmed
		}
		return nil
	}
	hello := sidExchange(t, s.auth, dispatchHello, 7)
	intent := sidExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent), 7)
	_ = sidExchange(t, s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"), 7)
	f.controls <- Stop
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-failed:
	case <-ctx.Done():
		t.Fatal("SCM owner never attempted runtime cleanup")
	}
	if snapshot := s.Snapshot(); snapshot.State != StopPending || snapshot.CleanupConfirmed || snapshot.HasAuthority || f.owner.finalized.Load() != 0 || f.target.closes.Load() != 1 {
		t.Error("unknown guest cleanup released outer ownership or replayed target close", snapshot)
	}
	select {
	case <-f.done:
		t.Error("SCM owner ended before cleanup acknowledgement")
	default:
	}
	recoverable.Store(true)
	select {
	case <-f.done:
	case <-ctx.Done():
		t.Fatal("same SCM owner required another Stop to retry cleanup")
	}
	if f.specific || f.code != 0 || !s.Snapshot().CleanupConfirmed || f.owner.closes.Load() < 2 || f.owner.finalized.Load() != 1 || f.target.closes.Load() != 1 || f.owner.prepares.Load() != 1 || f.target.commits.Load() != 1 {
		t.Fatal("cleanup retry repeated preparation/execution or lost final acknowledgement", f.code, f.owner.closes.Load())
	}
}
