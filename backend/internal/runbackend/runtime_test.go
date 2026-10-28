package runbackend_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

func approved(t *testing.T) runbackend.ApprovedConfig {
	t.Helper()
	// 非秘密の合成seed。実鍵・アカウント・鍵fileは作成しない。
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{11}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := backend.PolicyDigest("restricted")
	if err != nil {
		t.Fatal(err)
	}
	keyDigest, err := runbackend.KeySetDigest([]ssh.PublicKey{signer.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	id := backend.Identity{LaunchID: "synthetic-approved", Epoch: "1", PolicyDigest: policy}
	return runbackend.ApprovedConfig{
		Grant:   supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: id}, KeySetDigest: keyDigest, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: time.Now().Add(5 * time.Second), BackendLeaseCeiling: time.Second},
		Backend: backend.Config{Identity: id, OperationMode: "restricted", AllowedKeys: []ssh.PublicKey{signer.PublicKey()}, HostKey: signer, MaximumDuration: 3 * time.Second, LeaseDuration: time.Second, MaxChannels: 1, MaxHandshakes: 1},
		Wire:    control.WireContext{Generation: "1", Identity: control.WireIdentity{LaunchID: id.LaunchID, Epoch: id.Epoch, PolicyDigest: id.PolicyDigest}, Binding: control.Binding{ArtifactSHA256: string(bytes.Repeat([]byte{'a'}, 64)), ChannelNonce: string(bytes.Repeat([]byte{'b'}, 64))}, PolicyRef: "approved-policy"},
		Limits:  stdio.Limits{FrameBytes: control.FrameBytes, Frames: 16, CleanupTimeout: time.Second},
	}
}

type output struct{ bytes.Buffer }

func (*output) Close() error { return nil }

func frame(t *testing.T, c runbackend.ApprovedConfig, kind string, sequence uint64, payload string) []byte {
	t.Helper()
	m := control.Message{Protocol: control.Protocol, Kind: kind, Generation: c.Wire.Generation, Identity: c.Wire.Identity}
	if kind == "hello" {
		m.Binding = &c.Wire.Binding
	} else {
		m.Sequence, m.RequestID = strconv.FormatUint(sequence, 10), strconv.FormatUint(sequence, 10)
		m.Payload = json.RawMessage(payload)
	}
	b, err := control.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTrustedRuntimeUsesRealSessionThroughExistingStdio(t *testing.T) {
	c := approved(t)
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.Join([][]byte{frame(t, c, "hello", 0, ""), frame(t, c, "start", 1, `{"policyRef":"approved-policy"}`), frame(t, c, "snapshot", 2, `{}`), frame(t, c, "stop", 3, `{}`)}, nil)
	out := &output{}
	if err := r.Run(context.Background(), io.NopCloser(bytes.NewReader(input)), out); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})
	if len(lines) != 4 {
		t.Fatalf("stdio response count: %d", len(lines))
	}
	for n, want := range []string{"hello_ack", "receipt", "snapshot_result", "receipt"} {
		message, err := control.Decode(append(lines[n], '\n'))
		if err != nil || message.Kind != want {
			t.Fatalf("response %d: %s %v", n, message.Kind, err)
		}
		if n == 2 && !bytes.Contains(message.Payload, []byte(`"goState":"active"`)) {
			t.Fatalf("real Session was not active: %s", message.Payload)
		}
	}
	if s := r.Snapshot(); s.State != "terminated" || s.Identity != c.Grant.Binding.Identity {
		t.Fatalf("owned Session did not terminate: %+v", s)
	}
}

func TestEOFRevokesTheActivatedRealSession(t *testing.T) {
	c := approved(t)
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	input := append(frame(t, c, "hello", 0, ""), frame(t, c, "start", 1, `{"policyRef":"approved-policy"}`)...)
	if err := r.Run(context.Background(), io.NopCloser(bytes.NewReader(input)), &output{}); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().State != "terminated" {
		t.Fatal("EOF retained an activated Session")
	}
	if err := r.Run(context.Background(), io.NopCloser(bytes.NewReader(input)), &output{}); !errors.Is(err, runbackend.ErrAlreadyUsed) {
		t.Fatalf("EOF allowed control-channel resume: %v", err)
	}
}

func TestShortLeaseClosesBlockedControlInputWithoutRenewal(t *testing.T) {
	c := approved(t)
	c.Backend.LeaseDuration = 200 * time.Millisecond
	c.Grant.BackendLeaseCeiling = 300 * time.Millisecond
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), input, &output{}) }()
	if _, err := writer.Write(append(frame(t, c, "hello", 0, ""), frame(t, c, "start", 1, `{"policyRef":"approved-policy"}`)...)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || r.Snapshot().State != "terminated" || r.Snapshot().Reason != "lease-expired" {
			t.Fatalf("short lease did not revoke/close real Session: error=%v snapshot=%+v", err, r.Snapshot())
		}
	case <-time.After(2 * time.Second):
		writer.Close()
		t.Fatal("short lease did not close blocked control input")
	}
}

type finalOutput struct {
	writes  int
	once    sync.Once
	blocked chan struct{}
	closed  chan struct{}
}

func (w *finalOutput) Write(p []byte) (int, error) {
	w.writes++
	if w.writes < 3 {
		return len(p), nil
	}
	close(w.blocked)
	<-w.closed
	return 0, io.ErrClosedPipe
}
func (w *finalOutput) Close() error { w.once.Do(func() { close(w.closed) }); return nil }

func TestTerminatedSessionBoundsFinalControlOutput(t *testing.T) {
	c := approved(t)
	c.Limits.CleanupTimeout = 100 * time.Millisecond
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.Join([][]byte{frame(t, c, "hello", 0, ""), frame(t, c, "start", 1, `{"policyRef":"approved-policy"}`), frame(t, c, "stop", 2, `{}`)}, nil)
	out := &finalOutput{blocked: make(chan struct{}), closed: make(chan struct{})}
	defer out.Close()
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), io.NopCloser(bytes.NewReader(input)), out) }()
	select {
	case <-out.blocked:
	case <-time.After(time.Second):
		t.Fatal("final control output was not reached")
	}
	select {
	case err := <-done:
		if err == nil || r.Snapshot().State != "terminated" {
			t.Fatalf("unwritten final receipt became successful: %v %+v", err, r.Snapshot())
		}
	case <-time.After(time.Second):
		out.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("pure control fixture did not clean up")
		}
		t.Fatal("terminated Session left final control output blocked")
	}
}

func TestRuntimeRejectsUnboundOrExtendedInputs(t *testing.T) {
	for _, name := range []string{"generation", "identity", "wire", "policy", "keys", "guest", "maximum", "lease", "frame-budget", "cleanup-budget"} {
		t.Run(name, func(t *testing.T) {
			c := approved(t)
			switch name {
			case "generation":
				c.Grant.Binding.Generation = 0
			case "identity":
				c.Backend.Identity.Epoch = "old"
			case "wire":
				c.Wire.Generation = "2"
			case "policy":
				c.Backend.OperationMode = "full"
			case "keys":
				c.Grant.KeySetDigest[0] ^= 1
			case "guest":
				c.Grant.GuestBoundary = 2
			case "maximum":
				// 等号は許可されるため、時計の進行に頼らず1秒の超過を入力で確定する。
				c.Backend.MaximumDuration = 3 * time.Second
				c.Grant.HardDeadline = time.Now().Add(2 * time.Second)
			case "lease":
				c.Backend.LeaseDuration = c.Grant.BackendLeaseCeiling + time.Millisecond
			case "frame-budget":
				c.Limits.FrameBytes++
			case "cleanup-budget":
				c.Limits.CleanupTimeout = 0
			}
			r, err := runbackend.New(c)
			if r != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = r.Close(ctx)
			}
			if !errors.Is(err, runbackend.ErrConfig) {
				t.Fatalf("unbound/extended input accepted: %v", err)
			}
		})
	}
}

func TestInvalidAndCanceledPipeRunStillClosesOwnedSession(t *testing.T) {
	for _, name := range []string{"nil-input", "canceled", "invalid-frame"} {
		t.Run(name, func(t *testing.T) {
			c := approved(t)
			r, err := runbackend.New(c)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var input io.ReadCloser = io.NopCloser(bytes.NewReader([]byte("{invalid}\n")))
			if name == "nil-input" {
				input = nil
			} else if name == "canceled" {
				cancel()
			}
			if err := r.Run(ctx, input, &output{}); err == nil || r.Snapshot().State != "terminated" {
				t.Fatalf("failed pipe retained real Session: %v %+v", err, r.Snapshot())
			}
		})
	}
}

func TestKeySetDigestIsAnImmutableSetBinding(t *testing.T) {
	first := approved(t).Backend.AllowedKeys[0]
	secondSigner, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{12}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	second := secondSigner.PublicKey()
	for _, keys := range [][]ssh.PublicKey{{first, second}, {second, first}, {first, second, first}} {
		want, err := runbackend.KeySetDigest([]ssh.PublicKey{first, second})
		if err != nil {
			t.Fatal(err)
		}
		got, err := runbackend.KeySetDigest(keys)
		if err != nil || got != want {
			t.Fatal("key set order/duplicate changed its binding")
		}
	}
	one, _ := runbackend.KeySetDigest([]ssh.PublicKey{first})
	two, _ := runbackend.KeySetDigest([]ssh.PublicKey{first, second})
	if one == two {
		t.Fatal("added key did not change the grant binding")
	}
	if _, err := runbackend.KeySetDigest([]ssh.PublicKey{nil}); err == nil {
		t.Fatal("nil key accepted")
	}
}

func TestRuntimeDoesNotRetainMutableConfigAndExposesSameCoordinator(t *testing.T) {
	c := approved(t)
	original := c
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Backend.AllowedKeys[0] = nil
	c.Grant.Binding.Identity.Epoch = "old"
	c.Grant.HardDeadline = c.Grant.HardDeadline.Add(time.Hour)
	input := append(frame(t, original, "hello", 0, ""), frame(t, original, "start", 1, `{"policyRef":"approved-policy"}`)...)
	if r.Coordinator() == nil {
		t.Fatal("same-Session coordinator missing")
	}
	before, err := r.Coordinator().Snapshot(original.Grant.Binding.Identity)
	if err != nil || before.Identity != r.Snapshot().Identity {
		t.Fatal("coordinator owns another Session")
	}
	if err := r.Run(context.Background(), io.NopCloser(bytes.NewReader(input)), &output{}); err != nil {
		t.Fatal(err)
	}
	after, err := r.Coordinator().Snapshot(original.Grant.Binding.Identity)
	if err != nil || after.State != "terminated" || after != r.Snapshot() {
		t.Fatal("same-Session close was not visible through coordinator")
	}
}

func TestNilContextClosesOwnedSessionAndConsumesRuntime(t *testing.T) {
	c := approved(t)
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(nil, nil, nil); !errors.Is(err, runbackend.ErrConfig) || r.Snapshot().State != "terminated" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Close(ctx)
		t.Fatalf("invalid context retained an owned Session: %v", err)
	}
	if err := r.Run(context.Background(), io.NopCloser(bytes.NewReader(nil)), &output{}); !errors.Is(err, runbackend.ErrAlreadyUsed) {
		t.Fatalf("failed invocation became reusable: %v", err)
	}
}

func TestZeroRuntimeCannotRestoreSavedAuthority(t *testing.T) {
	var r runbackend.Runtime
	if err := json.Unmarshal([]byte(`{"used":false,"grant":{"Generation":1}}`), &r); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), nil, nil); !errors.Is(err, runbackend.ErrConfig) || r.Coordinator() != nil || r.Snapshot().State != "" {
		t.Fatal("saved state restored a runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Close(ctx); !errors.Is(err, runbackend.ErrConfig) {
		t.Fatalf("zero runtime close: %v", err)
	}
}

func TestCanceledCleanupBudgetStillRevokesRealSession(t *testing.T) {
	c := approved(t)
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer r.Close(ctx)
	if err := r.Coordinator().Start(ctx, c.Grant.Binding.Identity); err != nil {
		t.Fatal(err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := r.Close(canceled); err == nil || r.Snapshot().State != "revoking" {
		t.Fatalf("failed cleanup left authority active: %v %+v", err, r.Snapshot())
	}
}
