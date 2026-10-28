package localcontrol

import (
	"context"
	"strconv"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
)

// Exchange は1 pipe message = 1 JSONL frameでread/token/codec/writeを直列に行う。
// 接続はendpointの寿命中1回だけで、失敗・取消後に再接続しない。
func (e *Endpoint) Exchange(ctx context.Context) (result error) {
	if err := e.acquire(ctx); err != nil {
		return err
	}
	defer func() { e.gate <- struct{}{} }()
	if e.poisoned {
		return ErrFatal
	}
	if e.closed {
		return ErrClosed
	}
	operation, cancel, err := e.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() {
		if result != nil && !e.poisoned {
			if err := e.closeLocked(); err != nil {
				result = err
			}
		}
	}()
	if operation.Err() != nil {
		return ErrDenied
	}
	api := e.boundary.api
	if !e.connected {
		if err := api.connect(operation, e.handle); err != nil {
			return ErrDenied
		}
		e.connected = true
	}
	frame, err := api.read(operation, e.handle)
	if err != nil || operation.Err() != nil {
		return ErrDenied
	}
	if err := e.authenticateLocked(operation); err != nil {
		return err
	}
	// 認証前にcodec/stateやSessionへ入力しない。複数messageを結合もしない。
	message, err := control.Decode(frame)
	if err != nil {
		return ErrDenied
	}
	config := e.boundary.policy.config
	if message.Generation != strconv.FormatUint(config.Generation, 10) || message.Identity != (control.WireIdentity{LaunchID: config.Identity.LaunchID, Epoch: config.Identity.Epoch, PolicyDigest: config.Identity.PolicyDigest}) || (message.Kind != "hello" && message.Kind != "snapshot" && message.Kind != "stop") {
		return ErrDenied
	}
	response, err := e.boundary.handler.Handle(operation, frame)
	if err != nil {
		return ErrUnconfirmed
	}
	if _, err := control.Decode(response); err != nil {
		return ErrUnconfirmed
	}
	if operation.Err() != nil {
		return ErrUnconfirmed
	}
	if err := api.write(operation, e.handle, response); err != nil {
		return ErrUnconfirmed
	}
	return nil
}

func wireBoundary(config Config, wire control.WireContext, api kernel, coordinator *control.Coordinator) (*Boundary, error) {
	if wire.Generation != strconv.FormatUint(config.Generation, 10) || wire.Identity != (control.WireIdentity{LaunchID: config.Identity.LaunchID, Epoch: config.Identity.Epoch, PolicyDigest: config.Identity.PolicyDigest}) || wire.Binding.ChannelNonce != config.ChannelNonce {
		return nil, ErrConfig
	}
	adapter, err := control.NewAdapter(wire, coordinator)
	if err != nil {
		return nil, ErrConfig
	}
	return newBoundary(config, api, adapter)
}

// gateはI/O/tokenの所有、activeMuはCloseからの同期的取消だけを保護する。
func (e *Endpoint) beginOperation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	if e.stopping {
		return nil, nil, ErrDenied
	}
	operation, cancel := context.WithCancel(ctx)
	e.activeCancel = cancel
	return operation, func() {
		e.activeMu.Lock()
		e.activeCancel = nil
		e.activeMu.Unlock()
		cancel()
	}, nil
}
func (e *Endpoint) stopOperations() {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	e.stopping = true
	if e.activeCancel != nil {
		e.activeCancel()
	}
}
