package servicehost

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

type startupScopeProbe struct {
	t        *testing.T
	events   *[]string
	unlocked *bool
	failure  error
	closes   int
	budget   time.Duration
}

var scopeClosePanic = errors.New("合成scope回収panic")

func (*startupScopeProbe) Recheck(context.Context) error { return nil }
func (p *startupScopeProbe) Close(ctx context.Context) error {
	p.closes++
	*p.events = append(*p.events, "scope-close")
	deadline, finite := ctx.Deadline()
	if ctx.Err() != nil || !finite || time.Until(deadline) > p.budget {
		p.t.Error("scope回収に独立した既存cleanup予算を渡していない")
	}
	if *p.unlocked {
		return bootstrapauth.ErrFatal
	}
	if p.failure == scopeClosePanic {
		panic("合成scope回収panic")
	}
	return p.failure
}

func TestNativeStartupScopeClosedBeforeUnlock(t *testing.T) {
	for _, mode := range []string{"base-error", "partial-child", "canceled-startup", "publish-error"} {
		t.Run(mode, func(t *testing.T) {
			n, a, issued, pair, events := nativeSourceFixture(t)
			unlocked := false
			probe := &startupScopeProbe{t: t, events: events, unlocked: &unlocked, budget: n.cfg.Limits.CleanupTimeout}
			n.api.verify = func(context.Context, uintptr) (serviceProbe, error) { return probe, nil }
			if mode == "base-error" {
				base := n.base
				n.base = nativeBaseFunc(func(ctx context.Context, g supervisor.ProvisionedGrant) (ChildPlan, error) {
					plan, _ := base.PrepareChild(ctx, g)
					return plan, ErrRuntimeUnconfirmed
				})
			}
			pair.failure = ErrRuntimeUnconfirmed
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			launcher := NewJobRuntime(n)
			launcher.startChild = func(context.Context, childjob.Spec) (Process, error) {
				if mode == "canceled-startup" {
					cancel()
				}
				if mode != "publish-error" {
					return &cleanupChild{}, ErrRuntimeUnconfirmed
				}
				return &cleanupChild{}, nil
			}
			process, err := launcher.startIssuedWithThread(ctx, a, issued, func() { *events = append(*events, "lock") }, func() {
				*events = append(*events, "unlock")
				unlocked = true
			})
			if process == nil || err == nil {
				t.Fatalf("部分起動失敗の所有を保持しない: %v", err)
			}
			if probe.closes != 1 || !unlocked || slices.Index(*events, "scope-close") >= slices.Index(*events, "unlock") {
				t.Fatalf("元threadを解放する前にscopeを一回回収していない: %v closes=%d", *events, probe.closes)
			}
			cleanup, end := context.WithTimeout(context.Background(), time.Second)
			defer end()
			if err := process.Close(cleanup); errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal) || probe.closes != 1 {
				t.Fatalf("回収済みscopeをHostのthreadで再検査: %v closes=%d", err, probe.closes)
			}
		})
	}
}

func TestNativeStartupScopeFailureNeverUnlocksOrLosesFatal(t *testing.T) {
	for _, failure := range []error{bootstrapauth.ErrFatal, protectedkey.ErrFatal, ErrRuntimeUnconfirmed, scopeClosePanic} {
		t.Run(failure.Error(), func(t *testing.T) {
			n, a, issued, pair, events := nativeSourceFixture(t)
			unlocked := false
			probe := &startupScopeProbe{t: t, events: events, unlocked: &unlocked, budget: n.cfg.Limits.CleanupTimeout, failure: failure}
			n.api.verify = func(context.Context, uintptr) (serviceProbe, error) { return probe, nil }
			pair.failure = ErrRuntimeUnconfirmed
			launcher := NewJobRuntime(n)
			launcher.startChild = func(context.Context, childjob.Spec) (Process, error) { return &cleanupChild{}, nil }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			process, err := launcher.startIssuedWithThread(ctx, a, issued, func() {}, func() { unlocked = true })
			want := failure
			if failure == ErrRuntimeUnconfirmed || failure == scopeClosePanic {
				want = bootstrapauth.ErrFatal
			}
			if process == nil || !errors.Is(err, want) || unlocked || probe.closes != 1 {
				t.Fatalf("scope回収不明のthreadを再利用/再試行: err=%v unlocked=%t closes=%d", err, unlocked, probe.closes)
			}
			probe.failure = nil
			if err := process.Close(ctx); !errors.Is(err, want) || !errors.Is(err, ErrRuntimeUnconfirmed) {
				t.Fatalf("後続の所有回収でcached fatalを失った: %v", err)
			}
		})
	}
}
