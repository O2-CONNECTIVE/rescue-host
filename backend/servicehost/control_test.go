package servicehost_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func TestServiceStopCancelsReadAndWaitsForItsCompletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command servicehost.Control
	}{{"stop", servicehost.Stop}, {"shutdown", servicehost.Shutdown}} {
		t.Run(tc.name, func(t *testing.T) {
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			host := servicehost.New(provisioningFunc(func(ctx context.Context) (supervisor.ProvisionedGrant, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				return supervisor.ProvisionedGrant{}, ctx.Err()
			}))
			controls := make(chan servicehost.Control, 1)
			defer close(controls)
			states := make(chan servicehost.ServiceState)
			done := make(chan error, 1)
			go func() { done <- servicehost.Serve(host, controls, states) }()
			select {
			case state := <-states:
				if state != servicehost.StartPending {
					t.Fatalf("initial state: %v", state)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no startup status")
			}
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("read was not entered")
			}
			controls <- tc.command
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("stop did not cancel read")
			}
			select {
			case state := <-states:
				if state != servicehost.StopPending {
					t.Fatalf("stop state: %v", state)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no stopping status")
			}
			select {
			case err := <-done:
				t.Fatalf("returned before read completed: %v", err)
			default:
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("read completion was not observed")
			}
		})
	}
}

func TestServiceReturnsUnconfiguredFailure(t *testing.T) {
	if err := servicehost.Serve(nil, nil, nil); !errors.Is(err, servicehost.ErrProvisioningUnconfigured) {
		t.Fatalf("unconfigured service succeeded: %v", err)
	}
}

func TestStatusBackpressureAndInterrogateDoNotBlockStopOrReloadPolicy(t *testing.T) {
	entered := make(chan struct{})
	reads := 0
	host := servicehost.New(provisioningFunc(func(ctx context.Context) (supervisor.ProvisionedGrant, error) {
		reads++
		close(entered)
		<-ctx.Done()
		return supervisor.ProvisionedGrant{}, ctx.Err()
	}))
	controls := make(chan servicehost.Control, 3)
	defer close(controls)
	done := make(chan error, 1)
	go func() { done <- servicehost.Serve(host, controls, nil) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("read was not entered")
	}
	controls <- servicehost.Interrogate
	controls <- servicehost.Control(255)
	controls <- servicehost.Stop
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked status receiver prevented stop")
	}
	if reads != 1 {
		t.Fatalf("interrogate reloaded policy: %d", reads)
	}
}

func TestClosedControlInputCancelsOwnedRead(t *testing.T) {
	entered := make(chan struct{})
	host := servicehost.New(provisioningFunc(func(ctx context.Context) (supervisor.ProvisionedGrant, error) {
		close(entered)
		<-ctx.Done()
		return supervisor.ProvisionedGrant{}, ctx.Err()
	}))
	controls := make(chan servicehost.Control)
	done := make(chan error, 1)
	go func() { done <- servicehost.Serve(host, controls, nil) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(controls)
		t.Fatal("read was not entered")
	}
	close(controls)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed controls did not cancel read")
	}
}
