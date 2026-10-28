package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

type bootstrapFunc func(context.Context) (runbackend.ApprovedConfig, error)

func (f bootstrapFunc) ReadApproved(ctx context.Context) (runbackend.ApprovedConfig, error) {
	return f(ctx)
}

type ownedOutput struct {
	bytes.Buffer
	closed bool
}

func (o *ownedOutput) Close() error { o.closed = true; return nil }

// 公開された固定seedによる非秘密の試験鍵。実credentialの生成/保存/設定は行わない。
func approvedConfig(t *testing.T) runbackend.ApprovedConfig {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := backend.PolicyDigest("restricted")
	if err != nil {
		t.Fatal(err)
	}
	keys := []ssh.PublicKey{signer.PublicKey()}
	keyDigest, err := runbackend.KeySetDigest(keys)
	if err != nil {
		t.Fatal(err)
	}
	id := backend.Identity{LaunchID: "approved-test", Epoch: "1", PolicyDigest: digest}
	return runbackend.ApprovedConfig{
		Grant:   supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: id}, KeySetDigest: keyDigest, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: time.Now().Add(10 * time.Second), BackendLeaseCeiling: 2 * time.Second},
		Backend: backend.Config{Identity: id, OperationMode: "restricted", AllowedKeys: keys, HostKey: signer, MaximumDuration: time.Second, LeaseDuration: time.Second, MaxChannels: 1, MaxHandshakes: 1},
		Wire:    control.WireContext{Generation: "1", Identity: control.WireIdentity{LaunchID: id.LaunchID, Epoch: id.Epoch, PolicyDigest: id.PolicyDigest}, Binding: control.Binding{ArtifactSHA256: strings.Repeat("a", 64), ChannelNonce: strings.Repeat("b", 64)}, PolicyRef: "approved-policy"},
		Limits:  stdio.Limits{FrameBytes: control.FrameBytes, Frames: 8, CleanupTimeout: time.Second},
	}
}

func TestTypedCommandEntryRunsTheRealApprovedSession(t *testing.T) {
	config := approvedConfig(t)
	var input bytes.Buffer
	for _, m := range []control.Message{
		{Protocol: control.Protocol, Kind: "hello", Generation: config.Wire.Generation, Identity: config.Wire.Identity, Binding: &config.Wire.Binding},
		{Protocol: control.Protocol, Kind: "start", Generation: config.Wire.Generation, Identity: config.Wire.Identity, Sequence: "1", RequestID: "1", Payload: json.RawMessage(`{"policyRef":"approved-policy"}`)},
		{Protocol: control.Protocol, Kind: "stop", Generation: config.Wire.Generation, Identity: config.Wire.Identity, Sequence: "2", RequestID: "2", Payload: json.RawMessage(`{}`)},
	} {
		frame, err := control.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		input.Write(frame)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output := &ownedOutput{}
	var stderr bytes.Buffer
	code := execute(ctx, []string{"--supervised-internal"}, bootstrapFunc(func(context.Context) (runbackend.ApprovedConfig, error) { return config, nil }), io.NopCloser(&input), output, &stderr)
	if code != 0 || stderr.Len() != 0 || !output.closed {
		t.Fatalf("typed runtime did not finish: code=%d stderr=%q closed=%v", code, stderr.String(), output.closed)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("missing approved runtime responses: %d", len(lines))
	}
	for n, kind := range []string{"hello_ack", "receipt", "receipt"} {
		m, err := control.Decode(append(lines[n], '\n'))
		if err != nil || m.Kind != kind {
			t.Fatalf("response %d: %v %v", n, m.Kind, err)
		}
	}
}

func TestCommandCannotCreateApprovalFromFlagsOrFailedBootstrap(t *testing.T) {
	for _, name := range []string{"missing", "flag", "unbounded", "canceled", "bootstrap-error", "invalid-policy"} {
		t.Run(name, func(t *testing.T) {
			config := approvedConfig(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var callCtx context.Context = ctx
			args := []string{"--supervised-internal"}
			var source protectedBootstrap = bootstrapFunc(func(context.Context) (runbackend.ApprovedConfig, error) {
				if name == "bootstrap-error" {
					return config, errors.New("SECRET_TEST_BOOTSTRAP")
				}
				if name == "invalid-policy" {
					config.Grant.KeySetDigest = [32]byte{}
				}
				return config, nil
			})
			switch name {
			case "missing":
				source = nil
			case "flag":
				args = append(args, "--allow-unsafe")
			case "unbounded":
				callCtx = context.Background()
			case "canceled":
				cancel()
			}
			output := &ownedOutput{}
			var stderr bytes.Buffer
			if code := execute(callCtx, args, source, io.NopCloser(strings.NewReader("")), output, &stderr); code != 2 || output.Len() != 0 || stderr.String() != "製品の制御起動は未接続です\n" {
				t.Fatalf("bootstrap refusal leaked or succeeded: code=%d stdout=%q stderr=%q", code, output.String(), stderr.String())
			}
		})
	}
}
