package servicehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

type cleanupChild struct{ closes int }

func (*cleanupChild) Wait(context.Context) error    { return ErrRuntimeUnconfirmed }
func (c *cleanupChild) Close(context.Context) error { c.closes++; return nil }

type cleanupBootstrap struct{ fatal error }

func (*cleanupBootstrap) Publish(context.Context, *supervisor.Authority) error {
	return ErrRuntimeUnconfirmed
}
func (b *cleanupBootstrap) Close(context.Context) error { return b.fatal }

func TestJobReturnedProcessClosePreservesFatalCleanup(t *testing.T) {
	for _, fatal := range []error{bootstrapauth.ErrFatal, protectedkey.ErrFatal} {
		for _, mode := range []string{"partial-child", "control-nil", "forced-fallback"} {
			t.Run(fatal.Error()+"/"+mode, func(t *testing.T) {
				n, a, _, _, _ := nativeSourceFixture(t)
				launcher := NewJobRuntime(nativeBaseFunc(func(ctx context.Context, grant supervisor.ProvisionedGrant) (ChildPlan, error) {
					plan, err := n.base.PrepareChild(ctx, grant)
					plan.PreparedBootstrap = &cleanupBootstrap{fatal: fatal}
					return plan, err
				}))
				child := &cleanupChild{}
				launcher.startChild = func(context.Context, childjob.Spec) (Process, error) {
					if mode == "partial-child" {
						return child, ErrRuntimeUnconfirmed
					}
					return child, nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				process, err := launcher.Start(ctx, a)
				if process == nil || !errors.Is(err, ErrRuntimeUnconfirmed) {
					t.Fatalf("部分起動の所有が返らない: %v", err)
				}
				if mode == "control-nil" {
					// 返却された実所有者の防御分岐もProcess.Closeで検査する。
					process.(*jobProcess).control = nil
				}
				err = process.Close(ctx)
				if !errors.Is(err, fatal) || !errors.Is(err, ErrRuntimeUnconfirmed) || child.closes != 1 {
					t.Fatalf("強制回収でfatal分類を失った: err=%v closes=%d", err, child.closes)
				}
			})
		}
	}
}
