package servicehost_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

func ownerApprovedConfig(t *testing.T) runbackend.ApprovedConfig {
	t.Helper()
	// 非秘密の合成seedだけを使い、鍵fileや実認証情報は作らない。
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{21}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := backend.PolicyDigest("restricted")
	if err != nil {
		t.Fatal(err)
	}
	keyDigest, err := runbackend.KeySetDigest([]ssh.PublicKey{signer.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	id := backend.Identity{LaunchID: "owner-control-synthetic", Epoch: "1", PolicyDigest: digest}
	return runbackend.ApprovedConfig{
		Grant: supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: id}, KeySetDigest: keyDigest, GuestBoundary: supervisor.SeparatedNonAdminGuest,
			HardDeadline: time.Now().Add(5 * time.Second), BackendLeaseCeiling: time.Second},
		Backend: backend.Config{Identity: id, OperationMode: "restricted", AllowedKeys: []ssh.PublicKey{signer.PublicKey()}, HostKey: signer, MaximumDuration: 3 * time.Second, LeaseDuration: time.Second, MaxChannels: 1, MaxHandshakes: 1},
		Wire: control.WireContext{Generation: "1", Identity: control.WireIdentity{LaunchID: id.LaunchID, Epoch: id.Epoch, PolicyDigest: id.PolicyDigest},
			Binding: control.Binding{ArtifactSHA256: string(bytes.Repeat([]byte{'a'}, 64)), ChannelNonce: string(bytes.Repeat([]byte{'b'}, 64))}, PolicyRef: "owner-approved"},
		Limits: stdio.Limits{FrameBytes: control.FrameBytes, Frames: 128, CleanupTimeout: time.Second},
	}
}

type ownerObservedInput struct {
	*io.PipeWriter
	snapshot chan struct{}
	once     sync.Once
}

func (w *ownerObservedInput) Write(frame []byte) (int, error) {
	if message, err := control.Decode(frame); err == nil && message.Kind == "snapshot" {
		w.once.Do(func() { close(w.snapshot) })
	}
	return w.PipeWriter.Write(frame)
}

func ownerAndRuntime(t *testing.T, config runbackend.ApprovedConfig) (*servicehost.OwnedControl, *runbackend.Runtime, <-chan error, <-chan struct{}) {
	t.Helper()
	runtime, err := runbackend.New(config)
	if err != nil {
		t.Fatal(err)
	}
	childInput, ownerInput := io.Pipe()
	ownerOutput, childOutput := io.Pipe()
	observed := &ownerObservedInput{PipeWriter: ownerInput, snapshot: make(chan struct{})}
	client, err := servicehost.NewOwnedControl(servicehost.OwnedControlConfig{Wire: config.Wire, Input: observed, Output: ownerOutput, PollInterval: 10 * time.Millisecond, OperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(context.Background(), childInput, childOutput) }()
	t.Cleanup(func() {
		ownerInput.Close()
		ownerOutput.Close()
		childInput.Close()
		childOutput.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		runtime.Close(cleanup)
	})
	return client, runtime, done, observed.snapshot
}

func TestOwnerControlStartsAndStopsTheSameRealRuntime(t *testing.T) {
	client, runtime, done, _ := ownerAndRuntime(t, ownerApprovedConfig(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.Snapshot().State != "active" {
		t.Fatal("start receiptが本体Sessionの起動と一致しません")
	}
	if err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.Snapshot().State != "terminated" {
		t.Fatal("stop成功が本体Sessionの終了と一致しません")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("所有stdioが終了しませんでした")
	}
}

func TestOwnerControlPollDoesNotRenewTheRealSessionLease(t *testing.T) {
	config := ownerApprovedConfig(t)
	config.Backend.LeaseDuration = 200 * time.Millisecond
	config.Grant.BackendLeaseCeiling = 300 * time.Millisecond
	client, runtime, done, _ := ownerAndRuntime(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Wait(ctx); err == nil {
		t.Fatal("短lease喪失を通常成功へ変えました")
	}
	if snapshot := runtime.Snapshot(); snapshot.Reason != "lease-expired" || snapshot.State == "active" {
		t.Fatalf("pollを継続しても本体leaseが失効しません: %+v", snapshot)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("lease喪失後のruntime終了を確認できません")
	}
}

func TestOwnerControlWaitAllowsAFreshStopBudget(t *testing.T) {
	config := ownerApprovedConfig(t)
	config.Backend.LeaseDuration = 2 * time.Second
	config.Grant.BackendLeaseCeiling = 2 * time.Second
	client, _, _, observed := ownerAndRuntime(t, config)
	lifetime, cancelLifetime := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelLifetime()
	if err := client.Start(lifetime); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait(lifetime) }()
	select {
	case <-observed:
	case <-lifetime.Done():
		t.Fatal("Waitが本体runtimeへsnapshotを送りません")
	}
	stop, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := client.Stop(stop); err != nil {
		t.Fatalf("Waitがfresh stopを塞ぎました: %v", err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("確認済みstop後もWaitが成功しません: %v", err)
		}
	case <-lifetime.Done():
		t.Fatal("stop後にWaitが終了しません")
	}
}

type ownerMemoryInput struct {
	bytes.Buffer
	short  bool
	closes atomic.Int32
}

func (w *ownerMemoryInput) Write(frame []byte) (int, error) {
	if w.short {
		return w.Buffer.Write(frame[:len(frame)-1])
	}
	return w.Buffer.Write(frame)
}
func (w *ownerMemoryInput) Close() error { w.closes.Add(1); return nil }

type ownerMemoryOutput struct {
	io.Reader
	closes   atomic.Int32
	closeErr error
}

func (r *ownerMemoryOutput) Close() error { r.closes.Add(1); return r.closeErr }

func ownerResponse(wire control.WireContext, kind, sequence, request string, payload string) control.Message {
	message := control.Message{Protocol: control.Protocol, Kind: kind, Generation: wire.Generation, Identity: wire.Identity, Sequence: sequence, RequestID: request, Payload: json.RawMessage(payload)}
	if kind == "hello_ack" {
		binding := wire.Binding
		message.Binding = &binding
	}
	return message
}

func ownerResponseBytes(t *testing.T, messages ...control.Message) []byte {
	t.Helper()
	var output []byte
	for _, message := range messages {
		// 意図的に不正な応答も作るため、test入力生成だけはEncodeを通さない。
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, append(data, '\n')...)
	}
	return output
}

func ownerMemoryClient(t *testing.T, wire control.WireContext, output []byte, short bool) (*servicehost.OwnedControl, *ownerMemoryInput, *ownerMemoryOutput) {
	t.Helper()
	in := &ownerMemoryInput{short: short}
	out := &ownerMemoryOutput{Reader: bytes.NewReader(output)}
	client, err := servicehost.NewOwnedControl(servicehost.OwnedControlConfig{Wire: wire, Input: in, Output: out, PollInterval: time.Millisecond, OperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client, in, out
}

// 以下は既存control/1の拒否とIO失敗に対する回帰検査。子processは作らない。
func TestOwnerControlRejectsWrongFullIdentityAndChannelBinding(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	for _, fault := range []string{"version", "generation", "launch", "epoch", "policy", "artifact", "nonce"} {
		t.Run(fault, func(t *testing.T) {
			ack := ownerResponse(wire, "hello_ack", "", "", "")
			switch fault {
			case "version":
				ack.Protocol = "unknown/2"
			case "generation":
				ack.Generation = "2"
			case "launch":
				ack.Identity.LaunchID = "older-launch"
			case "epoch":
				ack.Identity.Epoch = "2"
			case "policy":
				ack.Identity.PolicyDigest = strings.Repeat("c", 64)
			case "artifact":
				ack.Binding.ArtifactSHA256 = strings.Repeat("c", 64)
			case "nonce":
				ack.Binding.ChannelNonce = strings.Repeat("c", 64)
			}
			client, input, output := ownerMemoryClient(t, wire, ownerResponseBytes(t, ack), false)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.Start(ctx); err == nil || input.closes.Load() != 1 || output.closes.Load() != 1 {
				t.Fatal("別起動の応答を受理、またはpipeを失効しませんでした")
			}
			written := input.Len()
			if err := client.Start(ctx); err == nil || input.Len() != written {
				t.Fatal("poisoned channelで再起動しました")
			}
		})
	}
}

func TestOwnerControlRejectsWrongReceiptAndIncomingSequenceWrap(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	for _, fault := range []string{"request", "sequence", "wrap", "operation", "accepted"} {
		t.Run(fault, func(t *testing.T) {
			ack := ownerResponse(wire, "hello_ack", "", "", "")
			receipt := ownerResponse(wire, "receipt", "1", "1", `{"operation":"start","accepted":true}`)
			switch fault {
			case "request":
				receipt.RequestID = "2"
			case "sequence":
				receipt.Sequence = "2"
			case "wrap":
				receipt.Sequence = "18446744073709551615"
			case "operation":
				receipt.Payload = json.RawMessage(`{"operation":"stop","accepted":true}`)
			case "accepted":
				receipt.Payload = json.RawMessage(`{"operation":"start","accepted":false}`)
			}
			client, _, _ := ownerMemoryClient(t, wire, ownerResponseBytes(t, ack, receipt), false)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.Start(ctx); err == nil {
				t.Fatal("不正なstart receiptを受理しました")
			}
		})
	}
}

func TestOwnerControlDoesNotRetryAPartialRequestWrite(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	client, input, output := ownerMemoryClient(t, wire, nil, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Start(ctx); err == nil || input.closes.Load() != 1 || output.closes.Load() != 1 {
		t.Fatal("partial writeを通常成功へ変えました")
	}
	written := input.Len()
	if err := client.Start(ctx); err == nil || input.Len() != written {
		t.Fatal("部分requestを再送しました")
	}
}

func TestOwnerControlStopRequiresConfirmedEmptyTerminalSnapshot(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	for _, fault := range []string{"unconfirmed", "owned", "eof", "oversize"} {
		t.Run(fault, func(t *testing.T) {
			messages := []control.Message{
				ownerResponse(wire, "hello_ack", "", "", ""),
				ownerResponse(wire, "receipt", "1", "1", `{"operation":"start","accepted":true}`),
				ownerResponse(wire, "receipt", "2", "2", `{"operation":"stop","accepted":true}`),
			}
			payload := `{"goState":"terminated","pending":"0","active":"0","owned":"0","goCloseConfirmed":false}`
			if fault == "owned" {
				payload = `{"goState":"terminated","pending":"0","active":"0","owned":"1","goCloseConfirmed":true}`
			}
			if fault != "eof" && fault != "oversize" {
				messages = append(messages, ownerResponse(wire, "snapshot_result", "3", "3", payload))
			}
			data := ownerResponseBytes(t, messages...)
			if fault == "oversize" {
				data = append(data, []byte(strings.Repeat("x", control.FrameBytes)+"\n")...)
			}
			client, _, _ := ownerMemoryClient(t, wire, data, false)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if err := client.Stop(ctx); err == nil {
				t.Fatal("receiptだけで全停止と誤認しました")
			}
		})
	}
}

type ownerBlockedOutput struct {
	closed chan struct{}
	once   sync.Once
}

func (r *ownerBlockedOutput) Read([]byte) (int, error) { <-r.closed; return 0, io.ErrClosedPipe }
func (r *ownerBlockedOutput) Close() error             { r.once.Do(func() { close(r.closed) }); return nil }

func TestOwnerControlReadTimeoutClosesAndJoinsCooperativeIO(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	input := &ownerMemoryInput{}
	output := &ownerBlockedOutput{closed: make(chan struct{})}
	client, err := servicehost.NewOwnedControl(servicehost.OwnedControlConfig{Wire: wire, Input: input, Output: output, PollInterval: time.Millisecond, OperationTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Start(ctx) }()
	select {
	case err := <-done:
		if err == nil || input.closes.Load() != 1 {
			t.Fatal("期限切れIOを成功へ変換しました")
		}
	case <-ctx.Done():
		output.Close()
		<-done
		t.Fatal("協調的IOの取消が有限予算を超えました")
	}
}

type ownerBlockedInput struct {
	closed chan struct{}
	once   sync.Once
}

func (w *ownerBlockedInput) Write([]byte) (int, error) { <-w.closed; return 0, io.ErrClosedPipe }
func (w *ownerBlockedInput) Close() error              { w.once.Do(func() { close(w.closed) }); return nil }

func TestOwnerControlWriteTimeoutClosesAndJoinsCooperativeIO(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	input := &ownerBlockedInput{closed: make(chan struct{})}
	output := &ownerMemoryOutput{Reader: bytes.NewReader(nil)}
	client, err := servicehost.NewOwnedControl(servicehost.OwnedControlConfig{Wire: wire, Input: input, Output: output, PollInterval: time.Millisecond, OperationTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Start(ctx) }()
	select {
	case err := <-done:
		if err == nil || output.closes.Load() != 1 {
			t.Fatal("背圧の解除を確認しませんでした")
		}
	case <-ctx.Done():
		input.Close()
		<-done
		t.Fatal("協調的writeの取消が有限予算を超えました")
	}
}

func TestOwnerControlCloseFailureIsNeverReportedAsConfirmed(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	client, input, output := ownerMemoryClient(t, wire, nil, false)
	output.closeErr = errors.New("SECRET_SYNTHETIC_CLOSE")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Start(ctx); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("不明なCloseを成功または未選別診断へ変換しました")
	}
	if err := client.Abort(ctx); err == nil || input.closes.Load() != 1 || output.closes.Load() != 1 {
		t.Fatal("閉鎖失敗したpipeの所有を通常終了へ変換しました")
	}
}

func TestOwnerControlRequiresFiniteBudgetBeforeConsumingStart(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	frames := ownerResponseBytes(t, ownerResponse(wire, "hello_ack", "", "", ""), ownerResponse(wire, "receipt", "1", "1", `{"operation":"start","accepted":true}`))
	client, input, _ := ownerMemoryClient(t, wire, frames, false)
	if err := client.Start(context.Background()); err == nil || input.Len() != 0 {
		t.Fatal("有限予算なしで承認済startを消費しました")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Abort(ctx); err != nil {
		t.Fatal(err)
	}
}

type ownerPanicInput struct {
	ownerMemoryInput
	writePanic, closePanic bool
}

func (w *ownerPanicInput) Write(frame []byte) (int, error) {
	if w.writePanic {
		panic("SECRET_SYNTHETIC_WRITE")
	}
	return w.ownerMemoryInput.Write(frame)
}
func (w *ownerPanicInput) Close() error {
	if w.closePanic {
		panic("SECRET_SYNTHETIC_CLOSE")
	}
	return w.ownerMemoryInput.Close()
}

func TestOwnerControlIOPanicKeepsCleanupFailureInsideTheOwnedBoundary(t *testing.T) {
	wire := ownerApprovedConfig(t).Wire
	for _, fault := range []string{"write", "close"} {
		t.Run(fault, func(t *testing.T) {
			input := &ownerPanicInput{writePanic: fault == "write", closePanic: fault == "close"}
			output := &ownerMemoryOutput{Reader: bytes.NewReader(nil)}
			client, err := servicehost.NewOwnedControl(servicehost.OwnedControlConfig{Wire: wire, Input: input, Output: output, PollInterval: time.Millisecond, OperationTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var escaped any
			func() {
				defer func() { escaped = recover() }()
				err = client.Start(ctx)
			}()
			if escaped != nil || err == nil || output.closes.Load() != 1 {
				t.Fatal("IO失敗が所有者へpanic流出、または後続pipe回収を省略しました")
			}
		})
	}
}
