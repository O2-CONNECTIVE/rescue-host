//go:build windows

package servicehost_test

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/sys/windows/svc"
)

// Execute をメモリ内 channel で直接呼ぶ。svc.Run/SCM/service 起動は呼ばない。
func TestSCMHandlerRefusesUnconfiguredHostAndIgnoresArguments(t *testing.T) {
	handler := servicehost.NewSCMHandler(nil)
	specific, code := handler.Execute([]string{"ignored", "--policy=untrusted"}, nil, make(chan svc.Status, 1))
	if !specific || code == 0 {
		t.Fatalf("unconfigured handler succeeded: specific=%v code=%d", specific, code)
	}
}

func TestSCMHandlerMapsInterrogateAndStopWithoutUsingReportedRunning(t *testing.T) {
	entered := make(chan struct{})
	host := servicehost.New(provisioningFunc(func(ctx context.Context) (supervisor.ProvisionedGrant, error) {
		close(entered)
		<-ctx.Done()
		return supervisor.ProvisionedGrant{}, ctx.Err()
	}))
	requests := make(chan svc.ChangeRequest, 2)
	defer close(requests)
	statuses := make(chan svc.Status)
	type result struct {
		specific bool
		code     uint32
	}
	done := make(chan result, 1)
	go func() {
		specific, code := servicehost.NewSCMHandler(host).Execute(nil, requests, statuses)
		done <- result{specific, code}
	}()
	for attempt := 0; attempt < 2; attempt++ {
		select {
		case status := <-statuses:
			if status.State != svc.StartPending || status.Accepts != 0 {
				t.Fatalf("unexpected authorization/status: %+v", status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no status")
		}
		if attempt == 0 {
			requests <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: svc.Status{State: svc.Running}}
		}
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("read was not entered")
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case got := <-done:
		if got.specific || got.code != 0 {
			t.Fatalf("cooperative stop: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not cancel read")
	}
}
