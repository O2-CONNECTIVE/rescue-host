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
