//go:build windows

package servicehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/sys/windows/svc"
)

func TestReadySCMDefaultRefusesBeforeAnyNativePreparation(t *testing.T) {
	h := NewSCMReadyHandler(nil)
	specific, code := h.Execute([]string{"--approve"}, nil, nil)
	if !specific || code != 1 {
		t.Fatal("unconfigured SCM handler accepted")
	}
	if err := RunReadySCM(nil); !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatal("unconfigured dispatcher accepted")
	}
}
func TestReadySCMAdapterUsesOnlyNativeControlFieldsAndConfirmedOwner(t *testing.T) {
	entered := make(chan struct{})
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		close(entered)
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	launcher := &lifetimeLauncher{wait: func(context.Context) error { t.Error("approval was fabricated"); return nil }, close: func(context.Context) error { return nil }}
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}
	service, err := newService(ServiceConfig{Provisioning: source, Launcher: launcher, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	service.callerGuard = func(context.Context) error { return nil }
	requests := make(chan svc.ChangeRequest, 1)
	statuses := make(chan svc.Status, 8)
	done := make(chan uint32, 1)
	go func() {
		specific, code := NewSCMReadyHandler(service).Execute([]string{"--approve", "synthetic-untrusted"}, requests, statuses)
		if specific {
			code++
		}
		done <- code
	}()
	select {
	case state := <-statuses:
		if state.State != svc.StartPending || state.Accepts != 0 || state.CheckPoint != 1 || state.WaitHint != 1000 {
			t.Fatalf("unexpected start status: %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("no start status")
	}
	select {
	case state := <-statuses:
		if state.State != svc.Running || state.Accepts != (svc.AcceptStop|svc.AcceptShutdown) {
			t.Fatalf("unexpected running status: %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("no running status")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no operator source wait")
	}
	if service.Snapshot().HasAuthority || service.Snapshot().Generation != 0 {
		t.Fatal("SCM args created approval")
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("confirmed SCM exit code=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("SCM owner not joined")
	}
}
