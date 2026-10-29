package serviceprovision

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func TestOperatorPolicyUsesHeldProtectedRecordNotMutableCache(t *testing.T) {
	p, _, _ := previewPreparedFixture(t)
	_, record, _, err := p.previewRecord(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	p.record.Profile.Keys.OperatorSID = "S-1-5-21-9-8-7-4321"
	policy, err := p.operatorSIDPolicy(rootContext(t))
	if err != nil || policy.OperatorSID != record.Profile.Keys.OperatorSID || policy.GuestSID != record.Profile.Keys.GuestSID {
		t.Fatal("policy adopted cache/candidate instead of held Root", err)
	}
	ctx, cancel := context.WithCancel(rootContext(t))
	cancel()
	if _, err = p.operatorSIDPolicy(ctx); err == nil {
		t.Fatal("canceled Root supplied policy")
	}
	p.accepted.Store(false)
	if _, err = p.operatorSIDPolicy(rootContext(t)); err == nil {
		t.Fatal("rejected Root supplied policy")
	}
}

func TestOriginalNativeOwnerSuppliesSIDPolicyBeforeServiceExposure(t *testing.T) {
	p, _, _ := previewPreparedFixture(t)
	e, err := NewNativePreviewExecution(previewExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := p.operatorSIDPolicy(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	stage := &nativeExecutionStage{cfg: e.cfg, preview: e.preview, calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }}}
	ready := make(chan executionReady, 1)
	go e.own(rootContext(t), func() executionStage { return stage }, ready)
	ctx := rootContext(t)
	var state executionReady
	select {
	case state = <-ready:
	case <-ctx.Done():
		t.Fatal("original owner not ready")
	}
	if state.err != nil || state.service == nil {
		t.Fatal(state.err)
	}
	// The source method has already bound policy on the original thread. A
	// second attempt can never replace it; no public readiness proof is added.
	if err := state.service.BindOperatorSIDPolicy(e, expected); !errors.Is(err, servicehost.ErrAlreadyUsed) {
		t.Fatal("original owner skipped protected policy registration")
	}
	if err := state.service.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.cleaned:
	case <-ctx.Done():
		t.Fatal("original owner did not release Root after inner join")
	}
	if stage.prepared != nil {
		t.Fatal("policy reference outlived outer Root cleanup")
	}
}

func TestOperatorSourceCannotBindForeignService(t *testing.T) {
	stage := &nativeExecutionStage{service: new(servicehost.Service)}
	if err := stage.BindOperatorSIDPolicy(rootContext(t), new(servicehost.Service), nil); err == nil {
		t.Fatal("foreign Service reached policy source")
	}
}

type failingSIDPolicyStage struct {
	*nativeExecutionStage
	reject    func()
	afterBind bool
	closed    atomic.Int32
}

func (s *failingSIDPolicyStage) BindOperatorSIDPolicy(ctx context.Context, service *servicehost.Service, owner servicehost.OuterFinalizer) error {
	if s.afterBind {
		if err := s.nativeExecutionStage.BindOperatorSIDPolicy(ctx, service, owner); err != nil {
			return err
		}
		s.reject()
		return ErrRootDenied
	}
	s.reject()
	return s.nativeExecutionStage.BindOperatorSIDPolicy(ctx, service, owner)
}
func (s *failingSIDPolicyStage) Close(ctx context.Context) error {
	s.closed.Add(1)
	return s.nativeExecutionStage.Close(ctx)
}

func TestOperatorPolicyFailureBeforeExposureJoinsOriginalOwnerCleanup(t *testing.T) {
	for _, kind := range []string{"record-rejected", "canceled", "canceled-after-binding"} {
		t.Run(kind, func(t *testing.T) {
			p, _, _ := previewPreparedFixture(t)
			e, err := NewNativePreviewExecution(previewExecutionConfig())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stage := &failingSIDPolicyStage{nativeExecutionStage: &nativeExecutionStage{cfg: e.cfg, preview: e.preview, calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }}}}
			stage.reject = func() { p.accepted.Store(false) }
			if kind != "record-rejected" {
				stage.reject = cancel
			}
			stage.afterBind = kind == "canceled-after-binding"
			e.calls = executionCalls{
				guard: func(context.Context, Anchor) error { return nil },
				stage: func() executionStage { return stage },
				runSCM: func(*servicehost.Service) error {
					t.Error("rejected policy exposed Service to SCM")
					return nil
				},
			}
			done := make(chan error, 1)
			go func() { done <- e.Run(ctx) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("policy failure reported startup success")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("policy failure did not join original owner cleanup")
			}
			if stage.closed.Load() != 1 || stage.prepared != nil || !stage.service.Snapshot().CleanupConfirmed {
				t.Fatal("startup failure lost root/inner/outer cleanup acknowledgement")
			}
			select {
			case <-e.cleaned:
			default:
				t.Fatal("Run returned before original owner cleanup")
			}
		})
	}
}
