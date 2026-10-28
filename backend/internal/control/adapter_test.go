package control

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

func approvedRealSession(t *testing.T) (*backend.Session, supervisor.ProvisionedGrant, WireContext) {
	t.Helper()
	// 公開の合成seedだけを使い、OS鍵や資格情報を生成・保存しない。
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := backend.PolicyDigest("restricted")
	if err != nil {
		t.Fatal(err)
	}
	wire := testWireContext()
	wire.Identity.PolicyDigest = digest
	grant := supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: wire.Identity.backend()}, KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: time.Now().Add(5 * time.Second), BackendLeaseCeiling: time.Second}
	s, err := backend.New(backend.Config{Identity: grant.Binding.Identity, OperationMode: "restricted", AllowedKeys: []ssh.PublicKey{signer.PublicKey()}, HostKey: signer, MaximumDuration: 5 * time.Second, LeaseDuration: time.Second, MaxChannels: 1, MaxHandshakes: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Errorf("real Session cleanup: %v", err)
		}
	})
	return s, grant, wire
}

func TestPreparedAdapterAcknowledgesRealSessionActivation(t *testing.T) {
	s, grant, wire := approvedRealSession(t)
	c, err := NewPrepared(s, grant, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAdapter(wire, c)
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, Message{Protocol: Protocol, Kind: "hello", Generation: wire.Generation, Identity: wire.Identity, Binding: &wire.Binding})
	message := Message{Protocol: Protocol, Kind: "start", Generation: wire.Generation, Identity: wire.Identity, Sequence: "1", RequestID: "1", Payload: json.RawMessage(`{"policyRef":"policy-001"}`)}
	if response := exchange(t, a, message); response.Kind != "receipt" || s.Snapshot().State != "active" {
		t.Fatalf("approved start lost its result: kind=%s state=%s", response.Kind, s.Snapshot().State)
	}
}

func TestCanceledPreparedStartCannotRetryOrProduceReceipt(t *testing.T) {
	s, grant, wire := approvedRealSession(t)
	c, err := NewPrepared(s, grant, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	if err := c.Start(ctx, grant.Binding.Identity); err == nil || s.Snapshot().State != "revoking" {
		t.Fatalf("canceled start remained usable: %v %+v", err, s.Snapshot())
	}
	a, err := NewAdapter(wire, c)
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, Message{Protocol: Protocol, Kind: "hello", Generation: wire.Generation, Identity: wire.Identity, Binding: &wire.Binding})
	message := Message{Protocol: Protocol, Kind: "start", Generation: wire.Generation, Identity: wire.Identity, Sequence: "1", RequestID: "1", Payload: json.RawMessage(`{"policyRef":"policy-001"}`)}
	if response := exchange(t, a, message); response.Kind != "error" || s.Snapshot().State != "revoking" {
		t.Fatal("failed start became a success or reactivated")
	}
}

func TestPreparedStartReplayDoesNotReactivateAfterStop(t *testing.T) {
	s, grant, wire := approvedRealSession(t)
	c, err := NewPrepared(s, grant, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAdapter(wire, c)
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, Message{Protocol: Protocol, Kind: "hello", Generation: wire.Generation, Identity: wire.Identity, Binding: &wire.Binding})
	request := Message{Protocol: Protocol, Kind: "start", Generation: wire.Generation, Identity: wire.Identity, Sequence: "1", RequestID: "1", Payload: json.RawMessage(`{"policyRef":"policy-001"}`)}
	if response := exchange(t, a, request); response.Kind != "receipt" {
		t.Fatal("first protected start failed")
	}
	stop := Message{Protocol: Protocol, Kind: "stop", Generation: wire.Generation, Identity: wire.Identity, Sequence: "2", RequestID: "2", Payload: json.RawMessage(`{}`)}
	if response := exchange(t, a, stop); response.Kind != "receipt" {
		t.Fatal("stop failed")
	}
	request.Sequence = "3"
	if response := exchange(t, a, request); response.Kind != "receipt" || s.Snapshot().State != "terminated" {
		t.Fatal("old start receipt became current activation")
	}
}

func testWireContext() WireContext {
	return WireContext{Generation: "1", Identity: WireIdentity{LaunchID: "launch-001", Epoch: "1", PolicyDigest: strings.Repeat("a", 64)}, Binding: Binding{ArtifactSHA256: strings.Repeat("b", 64), ChannelNonce: strings.Repeat("c", 64)}, PolicyRef: "policy-001"}
}

type countedSession struct {
	memorySession
	closes int
}

func (s *countedSession) Close(ctx context.Context) error {
	s.closes++
	err := s.memorySession.Close(ctx)
	if err == nil && s.snapshot.State == "terminated" {
		s.snapshot.Owned = 0
		s.snapshot.Pending = 0
		s.snapshot.Active = 0
	}
	return err
}
func boundAdapter(t *testing.T, s session) *Adapter {
	t.Helper()
	c, err := newCoordinator(s)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAdapter(testWireContext(), c)
	if err != nil {
		t.Fatal(err)
	}
	config := testWireContext()
	exchange(t, a, Message{Protocol: Protocol, Kind: "hello", Generation: config.Generation, Identity: config.Identity, Binding: &config.Binding})
	return a
}

func TestAdapterReplayDoesNotRepeatStopOrLeakError(t *testing.T) {
	config := testWireContext()
	s := &countedSession{memorySession: memorySession{snapshot: backend.Snapshot{Identity: config.Identity.backend(), State: "active"}}}
	a := boundAdapter(t, s)
	first := exchange(t, a, request(t, "stop", "1", "1", `{}`))
	replayed := exchange(t, a, request(t, "stop", "2", "1", `{}`))
	if first.Kind != "receipt" || replayed.Kind != "receipt" || replayed.Sequence != "2" || s.closes != 1 {
		t.Fatal("stop replay repeated side effects")
	}
	response := exchange(t, a, request(t, "snapshot", "3", "2", `{}`))
	if response.Kind != "snapshot_result" {
		t.Fatal("snapshot missing")
	}
	response = exchange(t, a, request(t, "snapshot", "4", "2", `{}`))
	if response.Kind != "error" || !bytes.Contains(response.Payload, []byte("REQUEST_REJECTED")) {
		t.Fatal("old snapshot id accepted")
	}
}

func TestNewStopIDIsNotAReplay(t *testing.T) {
	config := testWireContext()
	s := &countedSession{memorySession: memorySession{snapshot: backend.Snapshot{Identity: config.Identity.backend(), State: "active"}}}
	a := boundAdapter(t, s)
	exchange(t, a, request(t, "stop", "1", "1", `{}`))
	response := exchange(t, a, request(t, "stop", "2", "2", `{}`))
	if response.Kind != "receipt" || s.closes != 2 {
		t.Fatal("new stop id reused old acceptance")
	}
}

func TestRequestIDExhaustionTriggersOwnedCleanup(t *testing.T) {
	config := testWireContext()
	s := &memorySession{snapshot: backend.Snapshot{Identity: config.Identity.backend(), State: "active"}}
	a := boundAdapter(t, s)
	frame, _ := Encode(request(t, "snapshot", "1", "18446744073709551615", `{}`))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output := &outputBuffer{}
	err := stdio.Run(ctx, io.NopCloser(bytes.NewReader(frame)), output, stdio.Limits{FrameBytes: FrameBytes, Frames: 1, CleanupTimeout: time.Second}, a)
	if err != stdio.ErrService || !s.closed || output.Len() != 0 {
		t.Fatal("request id exhaustion did not close control channel before dispatch")
	}
}

func TestStateComparesLogicalReplayPayload(t *testing.T) {
	config := testWireContext()
	state, _ := NewState(config)
	for _, step := range []struct {
		direction Direction
		kind      string
	}{{OwnerToGo, "hello"}, {GoToOwner, "hello_ack"}} {
		if _, failure := state.Observe(step.direction, Message{Protocol: Protocol, Kind: step.kind, Generation: config.Generation, Identity: config.Identity, Binding: &config.Binding}); failure != nil {
			t.Fatal(failure)
		}
	}
	if _, failure := state.Observe(OwnerToGo, request(t, "start", "1", "1", `{"policyRef":"policy-001"}`)); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := state.Observe(GoToOwner, request(t, "receipt", "1", "1", `{"operation":"start","accepted":true}`)); failure != nil {
		t.Fatal(failure)
	}
	replay, failure := state.Observe(OwnerToGo, request(t, "start", "2", "1", `{"policyRef":"p\u006flicy-001"}`))
	if failure != nil || !replay {
		t.Fatal("equivalent decoded policyRef was not replayed")
	}
}

func TestAdapterUnconfirmedCloseIsNotReceipt(t *testing.T) {
	config := testWireContext()
	s := &memorySession{snapshot: backend.Snapshot{Identity: config.Identity.backend(), State: "active"}, closeError: errors.New("SECRET_TEST_CLOSE")}
	a := boundAdapter(t, s)
	response := exchange(t, a, request(t, "stop", "1", "1", `{}`))
	if response.Kind != "error" || !bytes.Contains(response.Payload, []byte("TERMINATION_UNCONFIRMED")) || bytes.Contains(response.Payload, []byte(`"goCloseConfirmed":true`)) {
		t.Fatal("unconfirmed close promoted to success")
	}
}

type outputBuffer struct{ bytes.Buffer }

func (*outputBuffer) Close() error { return nil }
func TestInvalidFrameClosesThroughOwnedLoop(t *testing.T) {
	config := testWireContext()
	s := &memorySession{snapshot: backend.Snapshot{Identity: config.Identity.backend(), State: "active"}}
	c, _ := newCoordinator(s)
	a, _ := NewAdapter(config, c)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output := &outputBuffer{}
	err := stdio.Run(ctx, io.NopCloser(strings.NewReader("{invalid}\n")), output, stdio.Limits{FrameBytes: FrameBytes, Frames: 1, CleanupTimeout: time.Second}, a)
	if err != stdio.ErrService || !s.closed || output.Len() != 0 {
		t.Fatal("invalid input was echoed or cleanup not confirmed")
	}
}

func TestStatePreservesStopCorrelationCapacity(t *testing.T) {
	config := testWireContext()
	state, _ := NewState(config)
	for _, step := range []struct {
		direction Direction
		kind      string
	}{{OwnerToGo, "hello"}, {GoToOwner, "hello_ack"}} {
		if _, failure := state.Observe(step.direction, Message{Protocol: Protocol, Kind: step.kind, Generation: config.Generation, Identity: config.Identity, Binding: &config.Binding}); failure != nil {
			t.Fatal(failure)
		}
	}
	for i := 1; i <= 15; i++ {
		n := strconv.Itoa(i)
		if _, failure := state.Observe(OwnerToGo, request(t, "snapshot", n, n, `{}`)); failure != nil {
			t.Fatal("normal correlation capacity too small")
		}
	}
	if _, failure := state.Observe(OwnerToGo, request(t, "snapshot", "16", "16", `{}`)); failure == nil || failure.Code != RequestRejected || failure.CloseChannel {
		t.Fatal("normal correlation overflow accepted")
	}
	if _, failure := state.Observe(OwnerToGo, request(t, "stop", "17", "17", `{}`)); failure != nil {
		t.Fatal("normal work consumed stop reservation")
	}
	if _, failure := state.Observe(OwnerToGo, request(t, "stop", "18", "18", `{}`)); failure == nil || failure.Code != RequestRejected {
		t.Fatal("stop reservation grew without bound")
	}
}

func request(t *testing.T, kind, sequence, id, payload string) Message {
	t.Helper()
	config := testWireContext()
	return Message{Protocol: Protocol, Kind: kind, Generation: config.Generation, Sequence: sequence, RequestID: id, Identity: config.Identity, Payload: json.RawMessage(payload)}
}
func exchange(t *testing.T, a *Adapter, message Message) Message {
	t.Helper()
	frame, err := Encode(message)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := a.Handle(ctx, frame)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte("SECRET_TEST")) {
		t.Fatal("raw reason/error reached wire")
	}
	result, err := Decode(output)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestAdapterMapsExistingDispatcherWithClosedStart(t *testing.T) {
	config := testWireContext()
	s := &countedSession{memorySession: memorySession{snapshot: backend.Snapshot{Identity: config.Identity.backend(), State: "active", Reason: "SECRET_TEST_REASON", Owned: 1}}}
	coordinator, _ := newCoordinator(s)
	a, err := NewAdapter(config, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	hello := Message{Protocol: Protocol, Kind: "hello", Generation: config.Generation, Identity: config.Identity, Binding: &config.Binding}
	if response := exchange(t, a, hello); response.Kind != "hello_ack" {
		t.Fatal("bootstrap not acknowledged")
	}
	if response := exchange(t, a, request(t, "start", "1", "1", `{"policyRef":"policy-001"}`)); response.Kind != "error" || !bytes.Contains(response.Payload, []byte("REQUEST_REJECTED")) || s.closed {
		t.Fatal("start gate was opened")
	}
	if response := exchange(t, a, request(t, "snapshot", "2", "2", `{}`)); response.Kind != "snapshot_result" || !bytes.Contains(response.Payload, []byte(`"goState":"active"`)) || bytes.Contains(response.Payload, []byte(`"goCloseConfirmed":true`)) {
		t.Fatal("snapshot authority mapping failed")
	}
	if response := exchange(t, a, request(t, "stop", "3", "3", `{}`)); response.Kind != "receipt" || !s.closed {
		t.Fatal("stop not mapped to Session close")
	}
	if response := exchange(t, a, request(t, "snapshot", "4", "4", `{}`)); !bytes.Contains(response.Payload, []byte(`"goState":"terminated"`)) || !bytes.Contains(response.Payload, []byte(`"goCloseConfirmed":true`)) || !bytes.Contains(response.Payload, []byte(`"owned":"0"`)) {
		t.Fatal("Go close evidence missing")
	}
}

func TestAdapterRequiresDeadlineBeforeAcceptance(t *testing.T) {
	config := testWireContext()
	s := &memorySession{snapshot: backend.Snapshot{Identity: config.Identity.backend(), State: "active"}}
	c, _ := newCoordinator(s)
	a, _ := NewAdapter(config, c)
	hello := Message{Protocol: Protocol, Kind: "hello", Generation: config.Generation, Identity: config.Identity, Binding: &config.Binding}
	frame, _ := Encode(hello)
	if output, err := a.Handle(context.Background(), frame); err != DeadlineExceeded || len(output) != 0 || s.closed {
		t.Fatal("unbounded invocation accepted")
	}
	if response := exchange(t, a, hello); response.Kind != "hello_ack" {
		t.Fatal("rejected invocation changed state")
	}
}
func TestSharedStateVectors(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/control-v1/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		StateCases []struct {
			ID    string
			Steps []struct {
				Direction Direction
				Frame     json.RawMessage
			}
			StateAccepted bool
			CloseChannel  bool
			Code          Code
		}
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.StateCases) != 6 {
		t.Fatal("state vector count changed")
	}
	for _, test := range cases.StateCases {
		t.Run(test.ID, func(t *testing.T) {
			state, err := NewState(testWireContext())
			if err != nil {
				t.Fatal(err)
			}
			var failure *Failure
			for _, step := range test.Steps {
				var frame bytes.Buffer
				if err := json.Compact(&frame, step.Frame); err != nil {
					t.Fatal(err)
				}
				message, err := Decode(append(frame.Bytes(), '\n'))
				if err != nil {
					t.Fatal("shared state shape rejected")
				}
				_, failure = state.Observe(step.Direction, message)
				if failure != nil {
					break
				}
			}
			if (failure == nil) != test.StateAccepted || failure != nil && (failure.Code != test.Code || failure.CloseChannel != test.CloseChannel) {
				t.Fatalf("state differs from shared vector: %v", failure)
			}
		})
	}
}
