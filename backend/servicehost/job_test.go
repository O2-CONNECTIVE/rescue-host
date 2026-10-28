package servicehost_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// 未設定bootstrapを理由に、OS APIへ進まないことだけを検査する。
func TestJobRuntimeRejectsMissingProtectedBootstrap(t *testing.T) {
	grant := provisionedPolicy()
	a, err := supervisor.New(supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: supervisor.Identity{LaunchID: "synthetic", Epoch: "1", PolicyDigest: grant.PolicyDigest}}, KeySetDigest: grant.KeySetDigest, GuestBoundary: grant.GuestBoundary, HardDeadline: grant.HardDeadline, BackendLeaseCeiling: grant.BackendLeaseCeiling}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	process, err := servicehost.NewJobRuntime(nil).Start(ctx, a)
	if process != nil || !errors.Is(err, servicehost.ErrProvisioningUnconfigured) {
		t.Fatalf("missing bootstrap advanced to process: %v %v", process, err)
	}
}

type protectedChildFunc func(context.Context, supervisor.ProvisionedGrant) (servicehost.ChildPlan, error)

func (f protectedChildFunc) PrepareChild(ctx context.Context, g supervisor.ProvisionedGrant) (servicehost.ChildPlan, error) {
	return f(ctx, g)
}

type childInput struct {
	bytes.Buffer
	closed bool
}

func (p *childInput) Close() error { p.closed = true; return nil }

func TestJobPlanMismatchPreservesPipeCleanupWithoutStartingAChild(t *testing.T) {
	for _, name := range []string{"generation", "identity", "artifact", "bootstrap"} {
		t.Run(name, func(t *testing.T) {
			p := provisionedPolicy()
			grant := supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: supervisor.Identity{LaunchID: "synthetic", Epoch: "1", PolicyDigest: p.PolicyDigest}}, KeySetDigest: p.KeySetDigest, GuestBoundary: p.GuestBoundary, HardDeadline: p.HardDeadline, BackendLeaseCeiling: p.BackendLeaseCeiling}
			a, err := supervisor.New(grant, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer a.Revoke(grant.Binding)
			hash := [32]byte{1}
			wire := control.WireContext{Generation: "1", Identity: control.WireIdentity{LaunchID: "synthetic", Epoch: "1", PolicyDigest: p.PolicyDigest}, Binding: control.Binding{ArtifactSHA256: hex.EncodeToString(hash[:]), ChannelNonce: strings.Repeat("b", 64)}, PolicyRef: "approved"}
			switch name {
			case "generation":
				wire.Generation = "2"
			case "identity":
				wire.Identity.Epoch = "2"
			case "artifact":
				wire.Binding.ArtifactSHA256 = strings.Repeat("c", 64)
			}
			input := &childInput{}
			client, err := servicehost.NewOwnedControl(servicehost.OwnedControlConfig{Wire: wire, Input: input, Output: io.NopCloser(bytes.NewReader(nil)), PollInterval: time.Millisecond, OperationTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			bootstrap := func(context.Context, supervisor.Binding) error {
				t.Error("rejected plan invoked bootstrap")
				return nil
			}
			if name == "bootstrap" {
				bootstrap = nil
			}
			runtime := servicehost.NewJobRuntime(protectedChildFunc(func(context.Context, supervisor.ProvisionedGrant) (servicehost.ChildPlan, error) {
				return servicehost.ChildPlan{Spec: childjob.Spec{Artifact: childjob.Artifact{SHA256: hash}}, Control: client, Bootstrap: bootstrap}, nil
			}))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			process, err := runtime.Start(ctx, a)
			if process == nil || !errors.Is(err, servicehost.ErrProvisioningRejected) {
				t.Fatalf("bad plan advanced/lost pipe: %v %v", process, err)
			}
			if err := process.Close(ctx); err != nil || !input.closed {
				t.Fatalf("rejected plan pipe cleanup failed: %v", err)
			}
		})
	}
}

func TestOwnedProcessWaitsForNormalExitInsteadOfKillingAfterGoClose(t *testing.T) {
	client, runtime, done, _ := ownerAndRuntime(t, ownerApprovedConfig(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waits, kills := 0, 0
	process, err := servicehost.OwnProcess(&processAPI{wait: func(ctx context.Context) error {
		waits++
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}, close: func(context.Context) error { kills++; return nil }}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if waits != 1 || kills != 0 || runtime.Snapshot().State != "terminated" {
		t.Fatalf("normal close skipped exit confirmation: waits=%d forced=%d state=%s", waits, kills, runtime.Snapshot().State)
	}
}

func TestOwnedProcessRetainsBudgetForForcedCleanupAfterExitTimeout(t *testing.T) {
	client, _, _, _ := ownerAndRuntime(t, ownerApprovedConfig(t))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waits, kills := 0, 0
	process, err := servicehost.OwnProcess(&processAPI{wait: func(ctx context.Context) error { waits++; <-ctx.Done(); return ctx.Err() }, close: func(ctx context.Context) error {
		kills++
		if ctx.Err() != nil {
			t.Error("no budget remained for forced cleanup")
		}
		return nil
	}}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(ctx); !errors.Is(err, servicehost.ErrRuntimeUnconfirmed) {
		t.Fatalf("exit failure did not remain unconfirmed: %v", err)
	}
	if waits != 1 || kills != 1 {
		t.Fatalf("exit failure lost owned cleanup: waits=%d forced=%d", waits, kills)
	}
}
