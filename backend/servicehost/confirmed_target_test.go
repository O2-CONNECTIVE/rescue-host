package servicehost

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

type confirmationTargetMemory struct {
	inspections, commits, closes atomic.Int32
	onInspect                    func()
	onCommit                     func(context.Context) error
	revoked                      atomic.Bool
}

func (m *confirmationTargetMemory) inspect(context.Context) error {
	m.inspections.Add(1)
	if m.onInspect != nil {
		m.onInspect()
	}
	return nil
}
func (m *confirmationTargetMemory) commit(ctx context.Context) error {
	m.commits.Add(1)
	if m.onCommit != nil {
		return m.onCommit(ctx)
	}
	return nil
}
func (m *confirmationTargetMemory) revoke() error               { m.revoked.Store(true); return nil }
func (m *confirmationTargetMemory) close(context.Context) error { m.closes.Add(1); return nil }

func TestConfirmationInspectsTargetBeforeIssuingSameIssuerChallenge(t *testing.T) {
	s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
	model := &confirmationTargetMemory{}
	model.onInspect = func() {
		if s.issuer.Snapshot().Generation != 0 {
			t.Error("inspection happened after issuance")
		}
	}
	target := &confirmationTarget{ops: model}
	grantRuntimeFixture(t, s, target)
	hello := previewExchange(t, a, dispatchHello)
	intent := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	approved := previewExchange(t, a, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"))
	if approved.Approval == nil || model.inspections.Load() != 1 || model.commits.Load() != 0 || s.issuer.Snapshot().Generation != 1 {
		t.Fatal("same-target inspection did not precede the actual issuer without execution", model.inspections.Load(), model.commits.Load(), approved)
	}
}

func confirmedTargetFixture(t *testing.T) (*Service, *confirmationTargetMemory, *confirmationRuntimeSource) {
	t.Helper()
	s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
	model := &confirmationTargetMemory{}
	source := grantRuntimeFixture(t, s, &confirmationTarget{ops: model})
	hello := previewExchange(t, a, dispatchHello)
	intent := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	approved := previewExchange(t, a, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"))
	if approved.Approval == nil {
		t.Fatal("fixture could not confirm", approved)
	}
	return s, model, source
}
func TestConfirmedTargetUsesActualIssuerLaunchExactlyOnce(t *testing.T) {
	s, model, _ := confirmedTargetFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	g := s.preview.grant
	if err := s.launchConfirmedTarget(ctx, g); err != nil {
		t.Fatal(err)
	}
	if model.commits.Load() != 1 || !s.issuer.Snapshot().Consumed || !s.issuer.Snapshot().HasAuthority {
		t.Fatal("not the actual issuer launch")
	}
	if s.launchConfirmedTarget(ctx, g) == nil || model.commits.Load() != 1 {
		t.Fatal("launch replayed")
	}
	s.requestStop()
	if !s.issuer.Snapshot().Closed || !model.revoked.Load() {
		t.Fatal("canonical and target revocation separated")
	}
	if s.launchConfirmedTarget(ctx, g) == nil {
		t.Fatal("revoked evidence reused")
	}
}
func TestConfirmedTargetRejectsUnapprovedForeignOrChangedEvidence(t *testing.T) {
	for _, kind := range []string{"no-grant", "copy", "target", "issuer", "owner", "proof", "expired", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			s, model, source := confirmedTargetFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			g := s.preview.grant
			switch kind {
			case "no-grant":
				g = nil
			case "copy":
				copy := *g
				g = &copy
			case "target":
				g.target = &confirmationTarget{ops: &confirmationTargetMemory{}}
			case "issuer":
				other, _ := ephemeral.New()
				defer other.Close()
				g.challenge, _ = other.Prepare(ephemeral.ApprovedPolicy{})
			case "owner":
				source.owner = &confirmationEvidenceOwner{}
			case "proof":
				g.confirmation.runtimeProof.target = &confirmationTarget{}
			case "expired":
				s.preview.descriptor.HardDeadline = time.Now().Add(-time.Second)
			case "canceled":
				cancel()
			}
			if err := s.launchConfirmedTarget(ctx, g); err == nil || model.commits.Load() != 0 {
				t.Fatal("unapproved or foreign evidence executed", err)
			}
			// Restore fixture-only mutations so the original SCM cleanup can verify ownership.
			if kind == "owner" {
				source.owner = g.confirmation.runtimeOwner
			}
		})
	}
}
func TestTargetInspectionFailureOrMismatchedProofCannotIssue(t *testing.T) {
	for _, kind := range []string{"inspect-error", "inspect-cancel", "foreign-target", "no-nonadmin"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
			model := &confirmationTargetMemory{}
			source := grantRuntimeFixture(t, s)
			source.target = &confirmationTarget{ops: model}
			source.owner.target = source.target
			original := source.kernel
			source.kernel = runtimeKernelModel(func(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
				p, err := original.Acquire(ctx, ticket)
				if p != nil {
					if kind == "foreign-target" {
						p.target = &confirmationTarget{}
					}
					if kind == "no-nonadmin" {
						p.boundary = 0
					}
				}
				return p, err
			})
			if kind == "inspect-error" {
				model.onInspect = func() { source.target.revoked.Store(true) }
			}
			if kind == "inspect-cancel" {
				model.onInspect = func() { s.requestStop() }
			}
			if err := s.registerConfirmationTarget(source); err != nil {
				t.Fatal(err)
			}
			hello := previewExchange(t, a, dispatchHello)
			intent := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := a.exchangeOps(ctx, fixtureAuthBatch(a, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil })
			if err == nil {
				t.Fatal("invalid evidence returned success")
			}
			if s.issuer.Snapshot().Generation != 0 || model.commits.Load() != 0 {
				t.Fatal("inspection or nonadmin proof failure issued/started")
			}
		})
	}
}

func TestTargetCannotLaunchBeforeConfirmation(t *testing.T) {
	s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
	model := &confirmationTargetMemory{}
	target := &confirmationTarget{ops: model}
	grantRuntimeFixture(t, s, target)
	hello := previewExchange(t, a, dispatchHello)
	_ = previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if s.launchConfirmedTarget(ctx, &confirmedGrant{target: target}) == nil || model.commits.Load() != 0 || s.issuer.Snapshot().Generation != 0 {
		t.Fatal("display intent or target pointer became approval")
	}
}

func TestStopDuringTargetCommitWaitsForActualReturnAndRejectsLateSuccess(t *testing.T) {
	s, model, source := confirmedTargetFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	model.onCommit = func(context.Context) error { close(entered); <-release; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	g := s.preview.grant
	go func() { result <- s.launchConfirmedTarget(ctx, g) }()
	<-entered
	s.requestStop()
	if !model.revoked.Load() || !s.issuer.Snapshot().Closed {
		t.Fatal("stop did not latch the same target and canonical issuer")
	}
	short, done := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := s.Close(short)
	done()
	if err == nil || s.Snapshot().CleanupConfirmed || model.closes.Load() != 0 {
		t.Fatal("pending resume was acknowledged or physically closed before return")
	}
	s.mu.Lock()
	retained := s.preview.runtime == source && source.target == g.target && g.launch != nil
	s.mu.Unlock()
	if !retained {
		t.Fatal("pending target ownership was discarded")
	}
	unblock()
	if err := <-result; err == nil {
		t.Fatal("late success bypassed canonical revocation")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if model.closes.Load() == 0 || !s.Snapshot().CleanupConfirmed {
		t.Fatal("original target was not reclaimed after launch joined")
	}
}

func TestRegisteredTargetIsClosedWhenStoppedBeforeConfirmation(t *testing.T) {
	s, _, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
	model := &confirmationTargetMemory{}
	grantRuntimeFixture(t, s, &confirmationTarget{ops: model})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if model.closes.Load() != 1 || !model.revoked.Load() {
		t.Fatal("registered suspended target lost before confirmation", model.closes.Load())
	}
}
func TestAcquireCannotClearOrReplaceOriginalTarget(t *testing.T) {
	for _, kind := range []string{"clear", "replace", "owner", "kernel", "preview-source"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
			model, foreign := &confirmationTargetMemory{}, &confirmationTargetMemory{}
			originalTarget := &confirmationTarget{ops: model}
			source := grantRuntimeFixture(t, s)
			source.target, source.owner.target = originalTarget, originalTarget
			original := source.kernel
			var originalJoins, foreignJoins atomic.Int32
			source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: runtimeKernelModel(func(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
				p, err := original.Acquire(ctx, ticket)
				switch kind {
				case "clear":
					source.target = nil
				case "replace":
					source.target = &confirmationTarget{ops: foreign}
				case "owner":
					source.owner = &confirmationEvidenceOwner{}
				case "kernel":
					source.kernel = nil
				}
				return p, err
			}), close: func(context.Context, *confirmationEvidenceOwner) error { originalJoins.Add(1); return nil }}
			if err := s.registerConfirmationTarget(source); err != nil {
				t.Fatal(err)
			}
			if kind == "preview-source" {
				other := grantRuntimeFixture(t, s)
				other.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: other.kernel, close: func(context.Context, *confirmationEvidenceOwner) error { foreignJoins.Add(1); return nil }}
			}
			hello := previewExchange(t, a, dispatchHello)
			intent := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = a.exchangeOps(ctx, fixtureAuthBatch(a, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil })
			if s.issuer.Snapshot().Generation != 0 {
				t.Error("acquisition downgraded into legacy authority")
			}
			_ = s.Close(ctx)
			if model.closes.Load() != 1 || !model.revoked.Load() || foreign.closes.Load() != 0 || originalJoins.Load() != 1 || foreignJoins.Load() != 0 {
				t.Error("cleanup retargeted or lost the originally registered actor", model.closes.Load(), originalJoins.Load(), foreignJoins.Load())
			}
		})
	}
}
func TestFatalServiceDoesNotPublishDoneBeforeTargetWorkerReturns(t *testing.T) {
	s, model, _ := confirmedTargetFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	model.onCommit = func(context.Context) error { close(entered); <-release; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.launchConfirmedTarget(ctx, s.preview.grant) }()
	<-entered
	s.stopFor(operatorauth.ErrFatal)
	observation, done := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	select {
	case <-s.done:
		t.Error("fatal Service published Done while target worker still owned execution")
	case <-observation.Done():
	}
	done()
	unblock()
	<-result
	select {
	case <-s.done:
	case <-ctx.Done():
		t.Fatal("fatal Service did not finish after worker returned")
	}
	if s.Snapshot().CleanupConfirmed {
		t.Fatal("fatal pending cleanup became success")
	}
}

func TestRegisteredActorSuccessIsNotReplayedAfterPlaneCleanupFailure(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
			source := grantRuntimeFixture(t, s)
			model := &confirmationTargetMemory{}
			source.target = &confirmationTarget{ops: model}
			source.owner.target = source.target
			var actorJoins, planeCloses atomic.Int32
			source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: source.kernel, close: func(context.Context, *confirmationEvidenceOwner) error {
				if actorJoins.Add(1) > 1 {
					return operatorauth.ErrFatal
				}
				return nil
			}}
			if err := s.registerConfirmationTarget(source); err != nil {
				t.Fatal(err)
			}
			if approved {
				hello := previewExchange(t, a, dispatchHello)
				intent := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
				_ = previewExchange(t, a, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"))
			}
			failed := make(chan struct{})
			s.control.witness.close = func(context.Context) error {
				if planeCloses.Add(1) == 1 {
					close(failed)
					return ErrRuntimeUnconfirmed
				}
				return nil
			}
			s.requestStop()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			select {
			case <-failed:
			case <-ctx.Done():
				t.Fatal("plane failure not reached")
			}
			if err := s.Close(ctx); err != nil {
				t.Error(err)
			}
			if actorJoins.Load() != 1 || model.closes.Load() != 1 || !s.Snapshot().CleanupConfirmed {
				t.Error("successful original actor cleanup was replayed", actorJoins.Load(), model.closes.Load())
			}
		})
	}
}
