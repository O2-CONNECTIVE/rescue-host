package serviceprovision

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func TestNativeExecutionRejectsUnconfiguredBeforeAcquisition(t *testing.T) {
	owner, err := NewNativeExecution(ExecutionConfig{})
	if owner != nil || !errors.Is(err, ErrRootConfig) {
		t.Fatalf("unconfigured execution accepted: owner=%t err=%v", owner != nil, err)
	}
}

type executionFakeStage struct {
	mu          sync.Mutex
	service     *servicehost.Service
	initErr     error
	calls       int
	entered     chan struct{}
	release     chan struct{}
	thread      uint64
	wrongThread bool
	initialize  func(context.Context)
}

func (s *executionFakeStage) Initialize(ctx context.Context) (*servicehost.Service, error) {
	s.mu.Lock()
	s.thread = executionTestThreadID()
	s.mu.Unlock()
	if s.initialize != nil {
		s.initialize(ctx)
	}
	return s.service, s.initErr
}
func (s *executionFakeStage) Close(context.Context) error {
	s.mu.Lock()
	s.calls++
	if s.thread != executionTestThreadID() {
		s.wrongThread = true
	}
	s.mu.Unlock()
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return nil
	default:
		return ErrRootUnconfirmed
	}
}
func executionFixture(t *testing.T, stage *executionFakeStage, dispatch func(*servicehost.Service) error) *Execution {
	t.Helper()
	e, err := NewNativeExecution(ExecutionConfig{Anchor: rootAnchor(), CleanupRetry: time.Millisecond, Approval: &noApprovalAtConstruction{}})
	if err != nil {
		t.Fatal(err)
	}
	e.calls = executionCalls{guard: func(context.Context, Anchor) error { return nil }, stage: func() executionStage { return stage }, runSCM: dispatch}
	return e
}
func executionNotReturned(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("returned with held resources: %v", err)
	default:
	}
}
func executionResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not finish")
		return nil
	}
}

func TestExecutionKeepsPartialOwnerUntilCleanupConfirmed(t *testing.T) {
	failure := errors.New("synthetic preparation failure")
	stage := &executionFakeStage{initErr: failure, entered: make(chan struct{}, 1), release: make(chan struct{})}
	e := executionFixture(t, stage, func(*servicehost.Service) error { t.Error("SCM entered after preparation failure"); return nil })
	done := make(chan error, 1)
	go func() { done <- e.Run(context.Background()) }()
	select {
	case <-stage.entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup never attempted")
	}
	executionNotReturned(t, done)
	close(stage.release)
	if err := executionResult(t, done); !errors.Is(err, failure) {
		t.Fatal("preparation failure lost:", err)
	}
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.wrongThread {
		t.Fatal("partial owner moved to another OS thread")
	}
	if stage.calls < 2 {
		t.Fatal("cleanup not retried")
	}
	if err := e.Run(context.Background()); !errors.Is(err, servicehost.ErrAlreadyUsed) {
		t.Fatal("execution reused:", err)
	}
}

func TestExecutionDispatcherFailureWaitsForOuterAcknowledgement(t *testing.T) {
	service, err := ComposeService(validRecord(), 123, &noApprovalAtConstruction{})
	if err != nil {
		t.Fatal(err)
	}
	stage := &executionFakeStage{service: service, entered: make(chan struct{}, 1), release: make(chan struct{})}
	failure := errors.New("synthetic dispatcher startup failure")
	e := executionFixture(t, stage, func(*servicehost.Service) error { return failure })
	done := make(chan error, 1)
	go func() { done <- e.Run(context.Background()) }()
	select {
	case <-stage.entered:
	case <-time.After(time.Second):
		t.Fatal("outer cleanup never attempted")
	}
	executionNotReturned(t, done)
	state := service.Snapshot()
	if state.State != servicehost.StopPending || state.CleanupConfirmed {
		t.Fatalf("SCM can report stopped before outer ack: %+v", state)
	}
	close(stage.release)
	if err := executionResult(t, done); !errors.Is(err, failure) {
		t.Fatal("dispatcher failure lost:", err)
	}
	if state := service.Snapshot(); state.State != servicehost.Stopped || !state.CleanupConfirmed {
		t.Fatal("outer acknowledgement lost:", state)
	}
}

func TestExecutionRejectsForgedProofWhileServiceBorrows(t *testing.T) {
	service, err := ComposeService(validRecord(), 123, &noApprovalAtConstruction{})
	if err != nil {
		t.Fatal(err)
	}
	stage := &executionFakeStage{service: service, entered: make(chan struct{}, 1), release: make(chan struct{})}
	dispatching, finish := make(chan struct{}), make(chan struct{})
	e := executionFixture(t, stage, func(*servicehost.Service) error { close(dispatching); <-finish; return nil })
	done := make(chan error, 1)
	go func() { done <- e.Run(context.Background()) }()
	select {
	case <-dispatching:
	case <-time.After(time.Second):
		t.Fatal("dispatcher not entered")
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	err = e.Finalize(cleanup, servicehost.InnerCleanupProof{})
	cancel()
	if !errors.Is(err, ErrRootUnconfirmed) {
		t.Fatal("forged proof accepted:", err)
	}
	stage.mu.Lock()
	calls := stage.calls
	stage.mu.Unlock()
	if calls != 0 {
		t.Fatal("outer owner closed while borrowed")
	}
	close(stage.release)
	close(finish)
	if err := executionResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestZeroExecutionRejectsBeforeAnyOwnerIsCreated(t *testing.T) {
	var e Execution
	if err := e.Run(context.Background()); !errors.Is(err, ErrRootConfig) {
		t.Fatal("zero owner not rejected as unconfigured:", err)
	}
}

func TestExecutionCancellationAfterCompositionDoesNotEnterSCM(t *testing.T) {
	service, err := ComposeService(validRecord(), 123, &noApprovalAtConstruction{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stage := &executionFakeStage{service: service, entered: make(chan struct{}, 1), release: make(chan struct{}), initialize: func(context.Context) { cancel() }}
	close(stage.release)
	e := executionFixture(t, stage, func(*servicehost.Service) error { t.Error("SCM entered after cancellation"); return nil })
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	if err := executionResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost:", err)
	}
	if state := service.Snapshot(); !state.CleanupConfirmed || state.HasAuthority {
		t.Fatal("unused inner borrower not stopped:", state)
	}
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.calls != 1 || stage.wrongThread {
		t.Fatal("cancelled composition owner not reclaimed on its thread")
	}
}

func TestExecutionFatalCallerRejectsBeforeWorkerAcquisition(t *testing.T) {
	e, err := NewNativeExecution(ExecutionConfig{Anchor: rootAnchor(), CleanupRetry: time.Millisecond, Approval: &noApprovalAtConstruction{}})
	if err != nil {
		t.Fatal(err)
	}
	e.calls = executionCalls{guard: func(context.Context, Anchor) error { return ErrRootFatal }, stage: func() executionStage { t.Error("fatal caller moved acquisition to clean worker"); return nil }, runSCM: func(*servicehost.Service) error { t.Error("fatal caller entered SCM"); return nil }}
	done := make(chan error, 1)
	// 専用goroutineをreturnし、native thread impersonation等は変更しない。
	go func() { done <- e.Run(context.Background()) }()
	if err := executionResult(t, done); !errors.Is(err, ErrRootFatal) {
		t.Fatal("fatal classification lost:", err)
	}
}
