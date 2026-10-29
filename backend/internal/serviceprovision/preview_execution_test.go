package serviceprovision

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func previewExecutionConfig() PreviewExecutionConfig {
	return PreviewExecutionConfig{Anchor: rootAnchor(), CleanupRetry: time.Millisecond,
		Limits: PreviewLimits{RequestLimit: 4, LaunchLimit: 5, IntentTTL: time.Second}}
}

func TestPreviewExecutionRejectsUnconfiguredWithoutLegacyFallback(t *testing.T) {
	for _, kind := range []string{"anchor", "retry", "requests", "launches", "ttl"} {
		t.Run(kind, func(t *testing.T) {
			c := previewExecutionConfig()
			switch kind {
			case "anchor":
				c.Anchor = Anchor{}
			case "retry":
				c.CleanupRetry = 0
			case "requests":
				c.Limits.RequestLimit = 0
			case "launches":
				c.Limits.LaunchLimit = c.Limits.RequestLimit
			case "ttl":
				c.Limits.IntentTTL = 0
			}
			if e, err := NewNativePreviewExecution(c); e != nil || err == nil {
				t.Fatal("unconfigured preview execution admitted")
			}
		})
	}
	if e, err := NewNativeExecution(ExecutionConfig{Anchor: rootAnchor(), CleanupRetry: time.Millisecond}); e != nil || !errors.Is(err, ErrApprovalUnconfigured) {
		t.Fatal("legacy missing approval acquired preview fallback", err)
	}
	if err := RunFixed(context.Background()); !errors.Is(err, ErrRootConfig) {
		t.Fatal("fixed unconfigured entry no longer refuses before native acquisition", err)
	}
}

func TestNativePreviewStageUsesProtectedRecordWithoutStrongTokenOrApproval(t *testing.T) {
	p, _, scope := previewPreparedFixture(t)
	limits := previewExecutionConfig().Limits
	var strongCalls int
	stage := &nativeExecutionStage{
		cfg:     ExecutionConfig{Anchor: rootAnchor(), Approval: &noApprovalAtConstruction{}},
		preview: &limits,
		calls: nativeExecutionCalls{
			prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil },
			launch: func(context.Context, *Prepared) (*LaunchToken, error) {
				strongCalls++
				return nil, ErrRootDenied
			},
		},
	}
	service, err := stage.Initialize(rootContext(t))
	if err != nil || service == nil || stage.token != nil || strongCalls != 0 || scope.rechecks != 1 {
		t.Fatal("preview stage did not end at query-only composition", err)
	}
	if s := service.Snapshot(); s.Generation != 0 || s.HasAuthority {
		t.Fatal("preview stage generated authority")
	}
	if err := service.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(rootContext(t)); err != nil || stage.prepared != nil || scope.closes != 1 {
		t.Fatal("same prepared owner was not closed", err)
	}
}

func TestNativePreviewStageRetainsPartialPreparedAndLegacyLaunchSelection(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "partial-preview"}[preview], func(t *testing.T) {
			p, _, scope := previewPreparedFixture(t)
			failure := errors.New("synthetic root or token refusal")
			limits := previewExecutionConfig().Limits
			strongCalls := 0
			stage := &nativeExecutionStage{cfg: ExecutionConfig{Anchor: rootAnchor()},
				calls: nativeExecutionCalls{
					prepare: func(context.Context, Anchor) (*Prepared, error) {
						if preview {
							return p, failure
						}
						return p, nil
					},
					launch: func(context.Context, *Prepared) (*LaunchToken, error) {
						strongCalls++
						return nil, failure
					},
				}}
			if preview {
				stage.preview = &limits
			}
			if service, err := stage.Initialize(rootContext(t)); service != nil || !errors.Is(err, failure) || stage.prepared != p {
				t.Fatal("failed preparation lost partial owner", err)
			}
			if (preview && strongCalls != 0) || (!preview && strongCalls != 1) {
				t.Fatal("preview and legacy token routes crossed")
			}
			if err := stage.Close(rootContext(t)); err != nil || scope.closes != 1 {
				t.Fatal(err)
			}
		})
	}
}

// The only modeled boundaries are protected root acquisition and the SCM call.
// Initialize/Close use the real native stage, constructor and owner RPC code.
type previewExecutionStageModel struct {
	native      nativeExecutionStage
	entered     chan struct{}
	release     chan struct{}
	thread      uint64
	wrongThread bool
}

func (s *previewExecutionStageModel) Initialize(ctx context.Context) (*servicehost.Service, error) {
	s.thread = executionTestThreadID()
	return s.native.Initialize(ctx)
}
func (s *previewExecutionStageModel) Close(ctx context.Context) error {
	if s.thread != executionTestThreadID() {
		s.wrongThread = true
	}
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return s.native.Close(ctx)
	default:
		return ErrRootUnconfirmed
	}
}

func TestPreviewExecutionDispatcherFailureKeepsOriginalOuterOwnerUntilAck(t *testing.T) {
	p, _, scope := previewPreparedFixture(t)
	c := previewExecutionConfig()
	e, err := NewNativePreviewExecution(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Limits.RequestLimit = 99
	if e.preview == nil || e.preview.RequestLimit != 4 || e.cfg.Approval != nil {
		t.Fatal("preview execution aliased caller input or installed legacy approval")
	}
	var strongCalls atomic.Int32
	stage := &previewExecutionStageModel{
		native: nativeExecutionStage{cfg: e.cfg, preview: e.preview,
			calls: nativeExecutionCalls{
				prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil },
				launch: func(context.Context, *Prepared) (*LaunchToken, error) {
					strongCalls.Add(1)
					return nil, ErrRootDenied
				},
			}},
		entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	failure := errors.New("synthetic preview SCM dispatch refusal")
	var dispatches atomic.Int32
	var dispatched atomic.Pointer[servicehost.Service]
	e.calls = executionCalls{
		guard: func(context.Context, Anchor) error { return nil },
		stage: func() executionStage { return stage },
		runSCM: func(actual *servicehost.Service) error {
			dispatched.Store(actual)
			dispatches.Add(1)
			return failure
		},
	}
	done := make(chan error, 1)
	runDone := make(chan struct{})
	go func() { defer close(runDone); done <- e.Run(context.Background()) }()
	released := false
	defer func() {
		if !released {
			close(stage.release)
		}
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
			t.Error("preview execution worker did not join")
		}
	}()
	select {
	case <-stage.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("original outer owner never received cleanup RPC")
	}
	executionNotReturned(t, done)
	service := dispatched.Load()
	if dispatches.Load() != 1 || service == nil ||
		service.Snapshot().State != servicehost.StopPending ||
		service.Snapshot().CleanupConfirmed || scope.closes != 0 {
		t.Fatal("preview failed to hold protected root before outer acknowledgement")
	}
	close(stage.release)
	released = true
	if err := executionResult(t, done); !errors.Is(err, failure) {
		t.Fatal("dispatch error lost", err)
	}
	if stage.wrongThread || stage.native.prepared != nil || scope.closes != 1 ||
		strongCalls.Load() != 0 || !service.Snapshot().CleanupConfirmed ||
		service.Snapshot().Generation != 0 || service.Snapshot().HasAuthority {
		t.Fatal("preview did not close its original root on the same thread or acquired authority")
	}
	if err := e.Run(context.Background()); !errors.Is(err, servicehost.ErrAlreadyUsed) {
		t.Fatal(err)
	}
}
