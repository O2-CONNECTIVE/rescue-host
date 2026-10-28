package servicehost

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
)

// OwnedControlConfigは同じ保護子へ既に束縛したpipeだけを受け取る。
// Wireのhash/nonce/Identityは相関であって相手認証やConfig/鍵のbootstrapではない。
// Closeがpending read/writeを解除して速やかに返ることが外部OS seamの必須条件。
// 任意adapterのhangや実Windows pipeの成立を、この型やmemory試験で保証しない。
type OwnedControlConfig struct {
	Wire                           control.WireContext
	Input                          io.WriteCloser
	Output                         io.ReadCloser
	PollInterval, OperationTimeout time.Duration
}

// OwnedControlはcontrol/1の所有者側client。初回利用後はコピーしない。
// pollは観測だけで、承認、lease更新、hard deadlineの延長には接続しない。
// 終了通知とpipe閉鎖は、別に所有するprocess/Jobの回収証明ではない。
type OwnedControl struct {
	config                                OwnedControlConfig
	state                                 *control.State
	reader                                *bufio.Reader
	gate                                  chan struct{}
	request                               uint64
	attempted, started, stopped, poisoned bool
	stopRequested                         atomic.Bool
	closed, closeFailed                   atomic.Bool
	closeOnce                             sync.Once
}

func NewOwnedControl(config OwnedControlConfig) (*OwnedControl, error) {
	if config.Input == nil || config.Output == nil || config.PollInterval <= 0 || config.OperationTimeout <= 0 {
		return nil, ErrProvisioningRejected
	}
	state, err := control.NewState(config.Wire)
	if err != nil {
		return nil, ErrProvisioningRejected
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &OwnedControl{config: config, state: state, reader: bufio.NewReaderSize(config.Output, control.FrameBytes), gate: gate}, nil
}

func controlBudget(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, finite := ctx.Deadline()
	return finite
}

func (c *OwnedControl) take(ctx context.Context) error {
	if c == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	select {
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	case <-c.gate:
	}
	if ctx.Err() != nil {
		c.gate <- struct{}{}
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (c *OwnedControl) closePipes() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		inputErr := controlCall(c.config.Input.Close)
		outputErr := controlCall(c.config.Output.Close)
		if inputErr != nil || outputErr != nil {
			c.closeFailed.Store(true)
		}
	})
	if c.closeFailed.Load() {
		// 失敗したpipeの参照をconfigに保持する。確認不能を成功へ変換しない。
		return ErrRuntimeUnconfirmed
	}
	return nil
}

// adapterのpanicでも所有者へ部分processの返却経路を残す。診断文字列は渡さない。
func controlCall(action func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return action()
}

// 操作中のwatcherは1本だけ。完了時に必ずjoinし、操作ごとの残存workerを作らない。
// IOの解除は借用したpipeのClose契約に依存し、任意の戻らないCloseは強制できない。
func (c *OwnedControl) operation(ctx context.Context, action func(context.Context) error) error {
	if err := c.take(ctx); err != nil {
		return err
	}
	defer func() { c.gate <- struct{}{} }()
	if c.poisoned {
		return ErrRuntimeUnconfirmed
	}
	budget, cancel := context.WithTimeout(ctx, c.config.OperationTimeout)
	defer cancel()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-budget.Done():
			_ = c.closePipes()
		case <-stop:
		}
	}()
	err := controlCall(func() error { return action(budget) })
	close(stop)
	<-done
	if budget.Err() != nil {
		err = ErrRuntimeUnconfirmed
	}
	if err != nil {
		c.poisoned = true
		_ = c.closePipes()
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (c *OwnedControl) exchange(ctx context.Context, request control.Message, expected string) (control.Message, error) {
	if ctx.Err() != nil || c.closed.Load() {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	if _, failure := c.state.Observe(control.OwnerToGo, request); failure != nil {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	frame, err := control.Encode(request)
	if err != nil {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	written, err := c.config.Input.Write(frame)
	if err != nil || written != len(frame) || ctx.Err() != nil {
		// partial writeの残りを再送せず、消費したsequence/request IDを再利用しない。
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	frame, err = c.reader.ReadSlice('\n')
	if err != nil || len(frame) > control.FrameBytes || ctx.Err() != nil {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	response, err := control.Decode(frame)
	if err != nil {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	if _, failure := c.state.Observe(control.GoToOwner, response); failure != nil {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	if response.Kind != expected || response.RequestID != request.RequestID || ctx.Err() != nil {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	return response, nil
}

func (c *OwnedControl) requestMessage(kind string, payload any) (control.Message, error) {
	// control.Stateも最大値を拒否する。加算前に拒否してwrap/reuseを防ぐ。
	if c.request >= math.MaxUint64-1 {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	c.request++
	body, err := json.Marshal(payload)
	if err != nil {
		return control.Message{}, ErrRuntimeUnconfirmed
	}
	number := strconv.FormatUint(c.request, 10)
	return control.Message{Protocol: control.Protocol, Kind: kind, Generation: c.config.Wire.Generation, Sequence: number, RequestID: number, Identity: c.config.Wire.Identity, Payload: body}, nil
}

func (c *OwnedControl) Start(ctx context.Context) error {
	return c.operation(ctx, func(budget context.Context) error {
		if c.attempted || c.stopped || c.stopRequested.Load() {
			return ErrRuntimeUnconfirmed
		}
		c.attempted = true
		wire := c.config.Wire
		hello := control.Message{Protocol: control.Protocol, Kind: "hello", Generation: wire.Generation, Identity: wire.Identity, Binding: &wire.Binding}
		if _, err := c.exchange(budget, hello, "hello_ack"); err != nil {
			return err
		}
		request, err := c.requestMessage("start", map[string]any{"policyRef": wire.PolicyRef})
		if err != nil {
			return err
		}
		if _, err := c.exchange(budget, request, "receipt"); err != nil {
			return err
		}
		c.started = true
		return nil
	})
}

type controlSnapshot struct {
	GoState, Pending, Active, Owned string
	GoCloseConfirmed                bool
}

func (c *OwnedControl) snapshot(ctx context.Context) (controlSnapshot, error) {
	request, err := c.requestMessage("snapshot", struct{}{})
	if err != nil {
		return controlSnapshot{}, err
	}
	response, err := c.exchange(ctx, request, "snapshot_result")
	if err != nil {
		return controlSnapshot{}, err
	}
	var snapshot controlSnapshot
	if err := json.Unmarshal(response.Payload, &snapshot); err != nil {
		return snapshot, ErrRuntimeUnconfirmed
	}
	return snapshot, nil
}

// Stop成功はGoのreceiptだけでなく、同じIdentityの終端snapshotとpipe閉鎖までを確認する。
func (c *OwnedControl) Stop(ctx context.Context) error {
	if c == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	c.stopRequested.Store(true)
	return c.operation(ctx, func(budget context.Context) error {
		if c.stopped {
			return nil
		}
		if !c.started {
			return ErrRuntimeUnconfirmed
		}
		request, err := c.requestMessage("stop", struct{}{})
		if err != nil {
			return err
		}
		if _, err := c.exchange(budget, request, "receipt"); err != nil {
			return err
		}
		snapshot, err := c.snapshot(budget)
		if err != nil || snapshot.GoState != "terminated" || !snapshot.GoCloseConfirmed || snapshot.Pending != "0" || snapshot.Active != "0" || snapshot.Owned != "0" {
			return ErrRuntimeUnconfirmed
		}
		if err := c.closePipes(); err != nil || budget.Err() != nil {
			return ErrRuntimeUnconfirmed
		}
		c.stopped = true
		return nil
	})
}

// Waitはsnapshot交換だけgateを保持し、poll待機中はStopの新しい予算を塞がない。
// 待機中の取消ではpipeを壊さず返す。進行中IOの取消はCloseで解除し未確認にする。
func (c *OwnedControl) Wait(ctx context.Context) error {
	if c == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	for {
		stopped := false
		err := c.operation(ctx, func(budget context.Context) error {
			if c.stopped {
				stopped = true
				return nil
			}
			if c.stopRequested.Load() {
				return nil
			}
			if !c.started {
				return ErrRuntimeUnconfirmed
			}
			snapshot, err := c.snapshot(budget)
			if err != nil || snapshot.GoState != "active" || snapshot.GoCloseConfirmed {
				return ErrRuntimeUnconfirmed
			}
			return nil
		})
		if err != nil || stopped {
			return err
		}
		timer := time.NewTimer(c.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Abortは認可やGo Closeの証明を作らず、所有pipeを閉じて進行中IOを解除するだけ。
func (c *OwnedControl) Abort(ctx context.Context) error {
	if c == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	c.stopRequested.Store(true)
	err := c.closePipes()
	if taken := c.take(ctx); taken != nil {
		return taken
	}
	defer func() { c.gate <- struct{}{} }()
	c.poisoned = true
	if err != nil || ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	return nil
}
