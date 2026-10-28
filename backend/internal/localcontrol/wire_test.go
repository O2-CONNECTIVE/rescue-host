package localcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
)

func TestOwnedPipeAuthenticatesHelloBeforeCodec(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}}
	dispatch := &fakeDispatcher{api: api}
	b, err := newBoundary(config, api, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := b.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	api.frame, err = control.Encode(control.Message{Protocol: control.Protocol, Kind: "hello", Generation: "1", Identity: control.WireIdentity{LaunchID: config.Identity.LaunchID, Epoch: config.Identity.Epoch, PolicyDigest: config.Identity.PolicyDigest}, Binding: &control.Binding{ArtifactSHA256: config.Identity.PolicyDigest, ChannelNonce: config.ChannelNonce}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	if dispatch.calls != 1 || api.connects != 1 || len(api.output) == 0 {
		t.Fatal("authenticated frame did not reach codec and owned response")
	}
}

func TestRejectedFrameClosesWithoutCodecOrReconnect(t *testing.T) {
	for _, name := range []string{"oversize", "partial", "two-frames", "unknown", "duplicate", "wrong-generation", "start", "guest", "read-failure", "connect-failure"} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			frame := []byte(`{"protocol":"rescue-host-control/1","kind":"snapshot","generation":"1","sequence":"1","requestId":"1","identity":{"launchId":"control-test","epoch":"1","policyDigest":"` + strings.Repeat("a", 64) + `"},"payload":{}}` + "\n")
			api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}}
			switch name {
			case "oversize":
				frame = bytes.Repeat([]byte("a"), control.FrameBytes+1)
			case "partial":
				frame = frame[:len(frame)-1]
			case "two-frames":
				frame = append(frame, frame...)
			case "unknown":
				frame = bytes.Replace(frame, []byte(`"payload":{}`), []byte(`"unknown":true,"payload":{}`), 1)
			case "duplicate":
				frame = bytes.Replace(frame, []byte(`"generation":"1"`), []byte(`"generation":"1","generation":"1"`), 1)
			case "wrong-generation":
				frame = bytes.Replace(frame, []byte(`"generation":"1"`), []byte(`"generation":"2"`), 1)
			case "start":
				frame = bytes.Replace(frame, []byte(`"snapshot"`), []byte(`"start"`), 1)
				frame = bytes.Replace(frame, []byte(`"payload":{}`), []byte(`"payload":{"policyRef":"p"}`), 1)
			case "guest":
				api.peer.sid = config.GuestSID
			case "read-failure":
				api.readError = errors.New("PRIVATE_READ_ERROR")
			case "connect-failure":
				api.connectError = errors.New("PRIVATE_CONNECT_ERROR")
			}
			api.frame = frame
			handler := &fakeDispatcher{api: api}
			b, _ := newBoundary(config, api, handler)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e, _ := b.open(ctx)
			if err := e.Exchange(ctx); err != ErrDenied || handler.calls != 0 || len(api.output) != 0 {
				t.Fatal("rejected input reached codec/response")
			}
			if err := e.Exchange(ctx); err != ErrClosed || api.connects != 1 {
				t.Fatal("failed channel resumed")
			}
			if _, err := b.open(ctx); err != ErrDenied {
				t.Fatal("factory opened a replacement connection")
			}
		})
	}
}

func TestCloseCancelsOwnedReadBeforeHandleRelease(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, readEntered: make(chan struct{})}
	handler := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	done := make(chan error, 1)
	go func() { done <- e.Exchange(ctx) }()
	select {
	case <-api.readEntered:
	case <-ctx.Done():
		t.Fatal("read did not start")
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != ErrDenied {
			t.Fatal("cancelled read succeeded")
		}
	case <-ctx.Done():
		t.Fatal("owned read did not return")
	}
	if handler.calls != 0 || api.events[len(api.events)-1] != "close" {
		t.Fatal("handle release preceded completed read")
	}
}

// 同じ共通Stateを利用する純粋handler。Session/鍵/listenerを生成しない。
type stateHandler struct{ state *control.State }

func (h stateHandler) Close(context.Context) error { return nil }

func (h stateHandler) Handle(_ context.Context, frame []byte) ([]byte, error) {
	m, err := control.Decode(frame)
	if err != nil {
		return nil, err
	}
	if _, failure := h.state.Observe(control.OwnerToGo, m); failure != nil {
		return nil, failure
	}
	if m.Kind == "hello" {
		m.Kind = "hello_ack"
	} else {
		m.Kind = "snapshot_result"
		m.Payload = json.RawMessage(`{"goState":"prepared","pending":"0","active":"0","owned":"0","goCloseConfirmed":false}`)
	}
	if _, failure := h.state.Observe(control.GoToOwner, m); failure != nil {
		return nil, failure
	}
	return control.Encode(m)
}

func TestOwnedPipePreservesSharedHelloSequenceAndNonresume(t *testing.T) {
	config := testConfig()
	wire := control.WireContext{Generation: strconv.FormatUint(config.Generation, 10), Identity: control.WireIdentity{LaunchID: config.Identity.LaunchID, Epoch: config.Identity.Epoch, PolicyDigest: config.Identity.PolicyDigest}, Binding: control.Binding{ArtifactSHA256: strings.Repeat("b", 64), ChannelNonce: config.ChannelNonce}, PolicyRef: "p"}
	state, err := control.NewState(wire)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}}
	b, _ := newBoundary(config, api, stateHandler{state})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	hello := control.Message{Protocol: control.Protocol, Kind: "hello", Generation: wire.Generation, Identity: wire.Identity, Binding: &wire.Binding}
	api.frame, _ = control.Encode(hello)
	if err := e.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	response, err := control.Decode(api.output)
	if err != nil || response.Kind != "hello_ack" {
		t.Fatal("hello binding missing")
	}
	request := control.Message{Protocol: control.Protocol, Kind: "snapshot", Generation: wire.Generation, Sequence: "1", RequestID: "1", Identity: wire.Identity, Payload: json.RawMessage(`{}`)}
	api.frame, _ = control.Encode(request)
	if err := e.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	response, err = control.Decode(api.output)
	if err != nil || response.Kind != "snapshot_result" || response.Sequence != "1" {
		t.Fatal("shared state response missing")
	}
	if err := e.Exchange(ctx); err != ErrUnconfirmed {
		t.Fatal("duplicate sequence was accepted")
	}
	if err := e.Exchange(ctx); err != ErrClosed || api.connects != 1 {
		t.Fatal("channel resumed after state failure")
	}
}

func TestPipeFailureClosesOwnedAdapterWithFreshBudget(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, readError: errors.New("PRIVATE_READ_ERROR")}
	handler := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	if err := e.Exchange(ctx); err != ErrDenied {
		t.Fatal(err)
	}
	if handler.closes != 1 || !handler.closeBudgetSeen {
		t.Fatal("owned adapter not closed with fresh finite budget")
	}
	if err := e.Close(ctx); err != nil || handler.closes != 1 {
		t.Fatal("cleanup repeated or lost")
	}
}

func TestCleanupFailureCannotBecomeClosedSuccess(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config}
	handler := &fakeDispatcher{api: api, closeError: errors.New("PRIVATE_CLOSE_ERROR")}
	b, _ := newBoundary(config, api, handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	if err := e.Close(ctx); err != ErrUnconfirmed {
		t.Fatal("cleanup failure reported closed success")
	}
	if err := e.Close(ctx); err != ErrUnconfirmed || handler.closes != 1 {
		t.Fatal("cleanup failure was forgotten or repeated")
	}
}

func TestResponseWriteFailureClosesAdapterWithoutRetry(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}, writeError: errors.New("PRIVATE_WRITE_ERROR")}
	handler := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	_, err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop})
	if err != ErrUnconfirmed || handler.calls != 1 || handler.closes != 1 {
		t.Fatal("response loss retried dispatch or omitted cleanup")
	}
	if err := e.Exchange(ctx); err != ErrClosed || handler.calls != 1 {
		t.Fatal("unconfirmed response resumed")
	}
}
