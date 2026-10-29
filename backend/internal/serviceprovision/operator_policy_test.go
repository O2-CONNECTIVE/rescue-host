package serviceprovision

import (
	"context"
	"errors"
	"testing"

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
