package control

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
)

type Adapter struct {
	state          *State
	coordinator    *Coordinator
	gate           chan struct{}
	closeConfirmed bool
}

func NewAdapter(config WireContext, coordinator *Coordinator) (*Adapter, error) {
	state, err := NewState(config)
	if err != nil {
		return nil, err
	}
	if _, err = coordinator.Snapshot(config.Identity.backend()); err != nil {
		return nil, ContextMismatch
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Adapter{state: state, coordinator: coordinator, gate: gate}, nil
}

var _ stdio.Service = (*Adapter)(nil)

func (a *Adapter) acquire(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return DeadlineExceeded
	}
	if _, ok := ctx.Deadline(); !ok {
		return DeadlineExceeded
	}
	select {
	case <-ctx.Done():
		return DeadlineExceeded
	case <-a.gate:
	}
	if ctx.Err() != nil {
		a.gate <- struct{}{}
		return DeadlineExceeded
	}
	return nil
}

// Handle は純粋なwire変換と既存dispatcher呼出しだけで、runtimeを生成しない。
func (a *Adapter) Handle(ctx context.Context, frame []byte) ([]byte, error) {
	if err := a.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { a.gate <- struct{}{} }()
	message, err := Decode(frame)
	if err != nil {
		a.closeChannel()
		return nil, err
	}
	replay, failure := a.state.Observe(OwnerToGo, message)
	if failure != nil {
		if failure.CloseChannel {
			return nil, failure
		}
		return a.reply(message, "error", map[string]any{"code": failure.Code, "retryable": false, "goCloseConfirmed": a.closeConfirmed})
	}
	if message.Kind == "hello" {
		config := a.state.config
		ack := Message{Protocol: Protocol, Kind: "hello_ack", Generation: config.Generation, Identity: config.Identity, Binding: &config.Binding}
		if _, failure := a.state.Observe(GoToOwner, ack); failure != nil {
			return nil, failure
		}
		return Encode(ack)
	}
	if replay {
		return a.reply(message, "receipt", map[string]any{"operation": message.Kind, "accepted": true})
	}
	id := message.Identity.backend()
	switch message.Kind {
	case "start":
		var payload struct{ PolicyRef string }
		_ = json.Unmarshal(message.Payload, &payload)
		if payload.PolicyRef == a.state.config.PolicyRef {
			_ = a.coordinator.Start(ctx, id)
		}
		return a.reply(message, "error", map[string]any{"code": RequestRejected, "retryable": false, "goCloseConfirmed": a.closeConfirmed})
	case "snapshot":
		snapshot, err := a.coordinator.Dispatch(ctx, ReadSnapshot, id)
		if err != nil {
			a.closeChannel()
			return nil, ContextMismatch
		}
		return a.reply(message, "snapshot_result", map[string]any{"goState": snapshot.State, "pending": strconv.Itoa(snapshot.Pending), "active": strconv.Itoa(snapshot.Active), "owned": strconv.Itoa(snapshot.Owned), "goCloseConfirmed": a.closeConfirmed && snapshot.State == "terminated"})
	case "stop":
		_, err := a.coordinator.Dispatch(ctx, Stop, id)
		if err != nil {
			code := TerminationUnconfirmed
			if ctx.Err() != nil || err == ErrBudget {
				code = DeadlineExceeded
			}
			if err == ErrIdentity {
				a.closeChannel()
				return nil, ContextMismatch
			}
			return a.reply(message, "error", map[string]any{"code": code, "retryable": false, "goCloseConfirmed": false})
		}
		a.closeConfirmed = true
		return a.reply(message, "receipt", map[string]any{"operation": "stop", "accepted": true})
	default:
		a.closeChannel()
		return nil, InvalidMessage
	}
}

func (a *Adapter) closeChannel() { a.state.mu.Lock(); a.state.closed = true; a.state.mu.Unlock() }

func (a *Adapter) reply(request Message, kind string, payload any) ([]byte, error) {
	a.state.mu.Lock()
	if a.state.closed || a.state.outSequence == math.MaxUint64 {
		a.state.closed = true
		a.state.mu.Unlock()
		return nil, ContextMismatch
	}
	sequence := a.state.outSequence + 1
	config := a.state.config
	a.state.mu.Unlock()
	body, err := json.Marshal(payload)
	if err != nil {
		a.closeChannel()
		return nil, InvalidMessage
	}
	message := Message{Protocol: Protocol, Kind: kind, Generation: config.Generation, Sequence: strconv.FormatUint(sequence, 10), RequestID: request.RequestID, Identity: config.Identity, Payload: body}
	if _, failure := a.state.Observe(GoToOwner, message); failure != nil {
		return nil, failure
	}
	return Encode(message)
}

// Close成功はGo内の終了確認だけで、所有processの回収は別の責務である。
func (a *Adapter) Close(ctx context.Context) error {
	a.closeChannel()
	if err := a.acquire(ctx); err != nil {
		return err
	}
	defer func() { a.gate <- struct{}{} }()
	if _, err := a.coordinator.Stop(ctx, a.state.config.Identity.backend()); err != nil {
		return TerminationUnconfirmed
	}
	a.closeConfirmed = true
	return nil
}

func (id WireIdentity) backend() backend.Identity {
	return backend.Identity{LaunchID: id.LaunchID, Epoch: id.Epoch, PolicyDigest: id.PolicyDigest}
}

// WireContext は保護所有者から事前に渡す期待値であり、helloから作らない。
// hash/nonceの照合はchannel bindingであり、同一userの認証ではない。
type WireContext struct {
	Generation string
	Identity   WireIdentity
	Binding    Binding
	PolicyRef  string
}
type Direction string

const (
	OwnerToGo Direction = "owner_to_go"
	GoToOwner Direction = "go_to_owner"
)

type Failure struct {
	Code         Code
	CloseChannel bool
}

func (f *Failure) Error() string { return string(f.Code) }

// State はenvelope/相関の純粋状態であり、認証や実行queueではない。
type State struct {
	mu                                  sync.Mutex
	config                              WireContext
	phase                               uint8
	closed                              bool
	inSequence, outSequence, maxRequest uint64
	pending                             map[string]Message
	receipts                            map[string]Message
	rejectedID                          string
}

func NewState(config WireContext) (*State, error) {
	if !asciiToken(config.PolicyRef) {
		return nil, InvalidMessage
	}
	if _, err := Encode(Message{Protocol: Protocol, Kind: "hello", Generation: config.Generation, Identity: config.Identity, Binding: &config.Binding}); err != nil {
		return nil, err
	}
	return &State{config: config, pending: make(map[string]Message), receipts: make(map[string]Message)}, nil
}

func (s *State) failure(code Code, closeChannel bool) (bool, *Failure) {
	if closeChannel {
		s.closed = true
	}
	return false, &Failure{code, closeChannel}
}

func (s *State) Observe(direction Direction, message Message) (bool, *Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.failure(ContextMismatch, true)
	}
	frame, err := Encode(message)
	if err != nil {
		code, _ := err.(Code)
		if code == "" {
			code = InvalidMessage
		}
		return s.failure(code, true)
	}
	// 所有する値へ複製し、呼出側のRawMessage変更を状態台帳へ伝播させない。
	message, err = Decode(frame)
	if err != nil {
		return s.failure(InvalidMessage, true)
	}
	if message.Generation != s.config.Generation || message.Identity != s.config.Identity {
		return s.failure(ContextMismatch, true)
	}
	if message.Kind == "hello" || message.Kind == "hello_ack" {
		if message.Binding == nil || *message.Binding != s.config.Binding {
			return s.failure(ContextMismatch, true)
		}
		if s.phase == 0 && direction == OwnerToGo && message.Kind == "hello" {
			s.phase = 1
			return false, nil
		}
		if s.phase == 1 && direction == GoToOwner && message.Kind == "hello_ack" {
			s.phase = 2
			return false, nil
		}
		return s.failure(ContextMismatch, true)
	}
	if s.phase != 2 {
		return s.failure(ContextMismatch, true)
	}
	var previous *uint64
	switch direction {
	case OwnerToGo:
		previous = &s.inSequence
	case GoToOwner:
		previous = &s.outSequence
	default:
		return s.failure(InvalidMessage, true)
	}
	sequence, _ := strconv.ParseUint(message.Sequence, 10, 64)
	if sequence == math.MaxUint64 || *previous == math.MaxUint64 || sequence != *previous+1 {
		return s.failure(ContextMismatch, true)
	}
	*previous = sequence
	if direction == OwnerToGo {
		return s.request(message)
	}
	return s.response(message)
}

func (s *State) request(message Message) (replayResult bool, failure *Failure) {
	defer func() {
		if failure != nil && !failure.CloseChannel {
			s.rejectedID = message.RequestID
		}
	}()
	if message.Kind != "start" && message.Kind != "snapshot" && message.Kind != "stop" {
		return s.failure(InvalidMessage, true)
	}
	id, _ := strconv.ParseUint(message.RequestID, 10, 64)
	// ID空間が尽きてからstopを拒否し続けず、loop所有者のcleanupへ進める。
	if id == math.MaxUint64 {
		return s.failure(ContextMismatch, true)
	}
	replay := false
	if id <= s.maxRequest {
		original, ok := s.receipts[message.Kind]
		if !ok || original.RequestID != message.RequestID || !samePayload(original, message) {
			return s.failure(RequestRejected, false)
		}
		replay = true
	} else {
		s.maxRequest = id
		if message.Kind == "start" {
			if _, ok := s.receipts["start"]; ok {
				return s.failure(RequestRejected, false)
			}
			if _, ok := s.receipts["stop"]; ok {
				return s.failure(RequestRejected, false)
			}
		}
	}
	if _, exists := s.pending[message.RequestID]; exists {
		return s.failure(RequestRejected, false)
	}
	var normal, stops, normalBytes, stopBytes int
	for _, pending := range s.pending {
		encoded, _ := Encode(pending)
		if pending.Kind == "stop" {
			stops++
			stopBytes += len(encoded)
		} else {
			normal++
			normalBytes += len(encoded)
		}
	}
	encoded, _ := Encode(message)
	if message.Kind == "stop" {
		if stops >= 1 || stopBytes+len(encoded) > 16384 {
			return s.failure(RequestRejected, false)
		}
	} else if normal >= 15 || normalBytes+len(encoded) > 49152 {
		return s.failure(RequestRejected, false)
	}
	s.pending[message.RequestID] = message
	return replay, nil
}

func samePayload(original, current Message) bool {
	if original.Kind != current.Kind {
		return false
	}
	if current.Kind == "stop" {
		return true
	}
	if current.Kind != "start" {
		return false
	}
	var first, second struct{ PolicyRef string }
	if json.Unmarshal(original.Payload, &first) != nil || json.Unmarshal(current.Payload, &second) != nil {
		return false
	}
	return first.PolicyRef == second.PolicyRef
}

func (s *State) response(message Message) (bool, *Failure) {
	if message.Kind == "state_changed" {
		return false, nil
	}
	if message.Kind == "error" && message.RequestID == s.rejectedID {
		s.rejectedID = ""
		return false, nil
	}
	request, ok := s.pending[message.RequestID]
	if !ok {
		return s.failure(ContextMismatch, true)
	}
	switch message.Kind {
	case "receipt":
		var payload struct{ Operation string }
		_ = json.Unmarshal(message.Payload, &payload)
		if request.Kind != payload.Operation || (request.Kind != "start" && request.Kind != "stop") {
			return s.failure(ContextMismatch, true)
		}
		if _, exists := s.receipts[request.Kind]; !exists {
			s.receipts[request.Kind] = request
		}
	case "snapshot_result":
		if request.Kind != "snapshot" {
			return s.failure(ContextMismatch, true)
		}
	case "error":
	default:
		return s.failure(InvalidMessage, true)
	}
	delete(s.pending, message.RequestID)
	return false, nil
}
