package servicehost_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

type provisioningFunc func(context.Context) (supervisor.ProvisionedGrant, error)

func (f provisioningFunc) ReadApproved(ctx context.Context) (supervisor.ProvisionedGrant, error) {
	return f(ctx)
}

func provisionedGrant() supervisor.ProvisionedGrant {
	return supervisor.ProvisionedGrant{
		Binding: supervisor.Binding{
			Generation: 1,
			Identity: supervisor.Identity{LaunchID: "approved-launch", Epoch: "1",
				PolicyDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		},
		KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest,
		HardDeadline: time.Now().Round(0).Add(time.Minute), BackendLeaseCeiling: 3 * time.Second,
	}
}

func TestUnconfiguredHostFailsClosed(t *testing.T) {
	host := servicehost.New(nil)
	if err := host.Run(context.Background()); !errors.Is(err, servicehost.ErrProvisioningUnconfigured) {
		t.Fatalf("unconfigured host did not refuse: %v", err)
	}
}

func TestApprovedReadStillCannotActivateAndRetainsImmutableGrant(t *testing.T) {
	input := provisionedGrant()
	original := input
	reads := 0
	host := servicehost.New(provisioningFunc(func(context.Context) (supervisor.ProvisionedGrant, error) {
		reads++
		return input, nil
	}))
	if err := host.Run(context.Background()); !errors.Is(err, servicehost.ErrActivationUnconfigured) {
		t.Fatalf("unconfigured activation was not refused: %v", err)
	}
	input.Binding.Generation = 2
	input.HardDeadline = input.HardDeadline.Add(time.Hour)
	for i := 0; i < 2; i++ {
		snapshot, ok := host.Snapshot()
		if !ok || snapshot.State != supervisor.Revoked || snapshot.Grant != original {
			t.Fatalf("grant changed or remained authorized after failed startup: %+v, present=%v", snapshot, ok)
		}
		snapshot.Grant.KeySetDigest[0] = 2
	}
	if reads != 1 {
		t.Fatalf("snapshot reloaded policy: reads=%d", reads)
	}
}

func TestHostDoesNotReloadOrRestartAfterRejectedActivation(t *testing.T) {
	reads := 0
	host := servicehost.New(provisioningFunc(func(context.Context) (supervisor.ProvisionedGrant, error) {
		reads++
		return provisionedGrant(), nil
	}))
	if err := host.Run(context.Background()); !errors.Is(err, servicehost.ErrActivationUnconfigured) {
		t.Fatal(err)
	}
	before, _ := host.Snapshot()
	if err := host.Run(context.Background()); !errors.Is(err, servicehost.ErrAlreadyUsed) {
		t.Fatalf("host attempted another startup: %v", err)
	}
	after, _ := host.Snapshot()
	if reads != 1 || after != before {
		t.Fatalf("host reloaded or changed grant: reads=%d", reads)
	}
}

func TestCancellationBeforeReadNeverLoadsPolicy(t *testing.T) {
	reads := 0
	host := servicehost.New(provisioningFunc(func(context.Context) (supervisor.ProvisionedGrant, error) {
		reads++
		return provisionedGrant(), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := host.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if _, present := host.Snapshot(); present || reads != 0 {
		t.Fatalf("canceled startup read policy: reads=%d, present=%v", reads, present)
	}
}

func TestLatePolicyAfterCancellationIsNotAccepted(t *testing.T) {
	entered := make(chan struct{})
	host := servicehost.New(provisioningFunc(func(ctx context.Context) (supervisor.ProvisionedGrant, error) {
		close(entered)
		<-ctx.Done()
		return provisionedGrant(), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("read was not entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late policy accepted: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cooperative read did not finish")
	}
	if _, present := host.Snapshot(); present {
		t.Fatal("late policy was retained as a grant")
	}
}

func TestProvisioningFailuresAreSanitizedAndDoNotRetainAGrant(t *testing.T) {
	for _, name := range []string{"error", "empty", "admin-unsupported", "expired"} {
		t.Run(name, func(t *testing.T) {
			host := servicehost.New(provisioningFunc(func(context.Context) (supervisor.ProvisionedGrant, error) {
				g := provisionedGrant()
				switch name {
				case "error":
					return g, errors.New("do-not-forward")
				case "empty":
					return supervisor.ProvisionedGrant{}, nil
				case "admin-unsupported":
					g.GuestBoundary = 2
				case "expired":
					g.HardDeadline = time.Now().Add(-time.Second)
				}
				return g, nil
			}))
			if err := host.Run(context.Background()); err != servicehost.ErrProvisioningRejected {
				t.Fatalf("unexpected error/diagnostic: %v", err)
			}
			if _, present := host.Snapshot(); present {
				t.Fatal("rejected provisioning retained a grant")
			}
		})
	}
}

func TestConcurrentRunAndSnapshotReadPolicyOnlyOnce(t *testing.T) {
	var reads atomic.Int32
	host := servicehost.New(provisioningFunc(func(context.Context) (supervisor.ProvisionedGrant, error) {
		reads.Add(1)
		return provisionedGrant(), nil
	}))
	var workers sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- host.Run(context.Background())
			for n := 0; n < 10; n++ {
				host.Snapshot()
			}
		}()
	}
	workers.Wait()
	close(results)
	activation, reused := 0, 0
	for err := range results {
		switch err {
		case servicehost.ErrActivationUnconfigured:
			activation++
		case servicehost.ErrAlreadyUsed:
			reused++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if activation != 1 || reused != 15 || reads.Load() != 1 {
		t.Fatalf("startup counts: activation=%d reused=%d reads=%d", activation, reused, reads.Load())
	}
}
