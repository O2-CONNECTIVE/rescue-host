package childjob

import (
	"context"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
)

func bootstrapSpec() Spec {
	s := approvedSpec()
	s.Bootstrap = &Bootstrap{ReadHandle: 30, Values: bootstrap.LaunchValues{IssuerInstanceEpoch: strings.Repeat("a", 64), Wire: control.WireContext{
		Generation: "1", Identity: control.WireIdentity{LaunchID: "native_1", Epoch: "1", PolicyDigest: strings.Repeat("b", 64)},
		Binding: control.Binding{ArtifactSHA256: hex.EncodeToString(s.Artifact.SHA256[:]), ChannelNonce: strings.Repeat("c", 64)}, PolicyRef: "approved",
	}}}
	return s
}

func TestBootstrapPlanRejectsAliasesAndMismatchesBeforeKernel(t *testing.T) {
	for name, change := range map[string]func(*Spec){
		"stdio alias": func(s *Spec) { s.Bootstrap.ReadHandle = s.Handles.Stdout },
		"token alias": func(s *Spec) { s.Bootstrap.ReadHandle = s.PrimaryToken },
		"null":        func(s *Spec) { s.Bootstrap.ReadHandle = 0 },
		"pseudo":      func(s *Spec) { s.Bootstrap.ReadHandle = ^uintptr(1) },
		"artifact":    func(s *Spec) { s.Bootstrap.Values.Wire.Binding.ArtifactSHA256 = strings.Repeat("f", 64) },
		"generation":  func(s *Spec) { s.Bootstrap.Values.Wire.Generation = "0" },
		"issuer":      func(s *Spec) { s.Bootstrap.Values.IssuerInstanceEpoch = "" },
	} {
		t.Run(name, func(t *testing.T) {
			s := bootstrapSpec()
			change(&s)
			api := &memoryKernel{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			child, err := start(ctx, s, api)
			if child != nil || err != ErrConfig || len(api.calls) != 0 {
				t.Fatalf("不一致をkernel呼出し前に拒否しない: %v %v", err, api.calls)
			}
		})
	}
}

type capturingKernel struct {
	*memoryKernel
	observed Spec
}

func (k *capturingKernel) createSuspended(s Spec) (uintptr, uintptr, error) {
	k.observed = s
	return k.memoryKernel.createSuspended(s)
}

func TestStartCopiesBootstrapBeforeAnyKernelBoundary(t *testing.T) {
	s := bootstrapSpec()
	original := *s.Bootstrap
	api := &capturingKernel{memoryKernel: &memoryKernel{onCall: func(op string) {
		if op == "verify" {
			s.Bootstrap.ReadHandle = 20
			s.Bootstrap.Values.Wire.Identity.LaunchID = "mutated"
		}
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	child, err := start(ctx, s, api)
	if err != nil || child == nil {
		t.Fatal(err)
	}
	if api.observed.Bootstrap == nil || *api.observed.Bootstrap != original {
		t.Fatal("callerの変更が準備済みbootstrapへ伝播した")
	}
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapPlanAddsOnlyReadEndpointAndFixedCorrelationArgs(t *testing.T) {
	s := bootstrapSpec()
	plan, err := makePlan(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.inheritedHandles(), []uintptr{10, 11, 12, 30}) {
		t.Fatalf("第4read handleを限定継承していない: %v", plan.inheritedHandles())
	}
	if !strings.Contains(plan.commandLine, " --bootstrap-handle=30 --bootstrap-issuer="+s.Bootstrap.Values.IssuerInstanceEpoch) || !strings.HasSuffix(plan.commandLine, " --bootstrap-policy-ref=approved") {
		t.Fatal("固定形式の相関引数を構成していない")
	}
}
