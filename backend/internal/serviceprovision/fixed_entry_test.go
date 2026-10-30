package serviceprovision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// モデル化するのは保護OS読取とSCMだけ。固定entryの選択、native stageの
// 構成、同じExecutionによる登録と回収は実コードを通す。
func TestFixedEntryLoadsRuntimeBundleBeforeSCMAndKeepsSameOwner(t *testing.T) {
	p, root, _, _ := runtimeConfigurationFixture(t)
	e, err := newFixedExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	loads, dispatches, strong := 0, 0, 0
	stage := &nativeExecutionStage{cfg: e.cfg, preview: e.preview, staging: e.staging, runtimeGuest: e.runtimeGuest, requestBudgets: e.requestBudgets,
		calls: nativeExecutionCalls{
			prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil },
			guest: func(ctx context.Context, got *Prepared) (*GuestConfiguration, error) {
				loads++
				if got != p {
					t.Error("protected owner replaced")
				}
				return got.loadRuntimeGuestConfiguration(ctx, root)
			},
			launch: func(context.Context, *Prepared) (*LaunchToken, error) {
				strong++
				return nil, ErrRootDenied
			},
		},
	}
	failure := errors.New("synthetic SCM refusal")
	var service *servicehost.Service
	e.calls = executionCalls{
		guard: func(context.Context, Anchor) error { return nil },
		stage: func() executionStage { return stage },
		runSCM: func(s *servicehost.Service) error {
			dispatches++
			service = s
			if loads != 1 || stage.guest == nil || !stage.guest.runtimeGuest {
				t.Error("fixed entry reached SCM without its runtime bundle")
			}
			if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
				t.Error("configuration created authority before authenticated confirmation")
			}
			return failure
		},
	}
	if err := e.Run(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if dispatches != 1 || loads != 1 || strong != 0 || service == nil || !service.Snapshot().CleanupConfirmed || stage.prepared != nil || stage.guest != nil {
		t.Fatal("fixed entry did not retain and reclaim the same source owner")
	}
}

func TestFixedEntryCanceledPreparationKeepsPartialUntilCleanupRetry(t *testing.T) {
	p, _, _, _ := runtimeConfigurationFixture(t)
	e, err := newFixedExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	entered, prepared := make(chan struct{}), make(chan struct{})
	stage := &previewExecutionStageModel{
		native: nativeExecutionStage{cfg: e.cfg, preview: e.preview, runtimeGuest: e.runtimeGuest, requestBudgets: e.requestBudgets,
			calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) {
				close(entered)
				<-prepared
				return p, nil // 取消前から所有する部分取得を遅れて返す。
			}}},
		entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	e.calls = executionCalls{guard: func(context.Context, Anchor) error { return nil },
		stage:  func() executionStage { return stage },
		runSCM: func(*servicehost.Service) error { t.Error("SCM entered after cancellation"); return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	preparedReleased, cleanupReleased := false, false
	defer func() {
		if !preparedReleased {
			close(prepared)
		}
		if !cleanupReleased {
			close(stage.release)
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("fixed entry did not prepare")
	}
	cancel()
	executionNotReturned(t, done)
	close(prepared)
	preparedReleased = true
	select {
	case <-stage.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("partial owner did not reach cleanup")
	}
	executionNotReturned(t, done)
	close(stage.release)
	cleanupReleased = true
	if err := executionResult(t, done); err == nil {
		t.Fatal("canceled preparation reported success")
	}
	if stage.native.prepared != nil || stage.wrongThread {
		t.Fatal("partial owner lost or reclaimed on another thread")
	}
	if err := e.Run(context.Background()); !errors.Is(err, servicehost.ErrAlreadyUsed) {
		t.Fatal("canceled fixed entry reused", err)
	}
}

func TestFixedEntryRejectsPartialConfigurationWithoutNativeFallback(t *testing.T) {
	for _, kind := range []string{"empty", "anchor", "retry", "limits", "requests"} {
		t.Run(kind, func(t *testing.T) {
			c := runtimeExecutionConfig()
			switch kind {
			case "empty":
				c = PreviewExecutionConfig{}
			case "anchor":
				c.Anchor = Anchor{}
			case "retry":
				c.CleanupRetry = 0
			case "limits":
				c.Limits = PreviewLimits{}
			case "requests":
				c.RuntimeRequests = servicehost.RequestLoopBudgets{}
			}
			if e, err := newFixedExecution(c); err == nil || e != nil {
				t.Fatal("partial fixed configuration acquired an execution owner")
			}
		})
	}
	for _, ctx := range []context.Context{nil, func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }()} {
		if err := RunFixed(ctx); !errors.Is(err, ErrApprovalUnconfigured) {
			t.Fatal("invalid caller context admitted", err)
		}
	}
}
