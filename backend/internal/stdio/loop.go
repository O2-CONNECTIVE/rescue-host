// Package stdio は製品 schema を持たない、所有 pipe の入出力基盤である。
package stdio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	ErrConfig  = errors.New("入出力基盤の設定が不正です")
	ErrInput   = errors.New("制御入力を受理できません")
	ErrLimit   = errors.New("入出力の上限を超えました")
	ErrService = errors.New("制御処理を確認できません")
	ErrOutput  = errors.New("制御出力を確認できません")
	ErrCleanup = errors.New("所有資源の終了を確認できません")
)

type Limits struct {
	FrameBytes, Frames int
	CleanupTimeout     time.Duration
}

// Service は decode・状態判断を担う。呼出しは直列で、context の取消に従う。
// Close は返却前に所有資源を回収し、context の期限に従う必要がある。
type Service interface {
	Handle(context.Context, []byte) ([]byte, error)
	Close(context.Context) error
}

// Run は検証済みの所有 pipe を引き取る。Close が並行 I/O を解除することが前提。
// 不正な設定では所有を移さない。wire の decode/検証は Service へ委譲する。
// 任意の戻らない adapter や Service に期限を強制する supervisor ではない。
func Run(ctx context.Context, input io.ReadCloser, output io.WriteCloser, limits Limits, service Service) (result error) {
	if ctx == nil || input == nil || output == nil || service == nil || limits.FrameBytes < 1 || limits.Frames < 1 || limits.CleanupTimeout <= 0 {
		return ErrConfig
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrConfig
	}
	var once sync.Once
	var closeError bool
	closePipes := func() {
		once.Do(func() {
			inErr := input.Close()
			outErr := output.Close()
			closeError = inErr != nil || outErr != nil
		})
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			closePipes()
		case <-stop:
		}
	}()
	defer func() {
		// 正常EOFは全Service所有者のbarrier後にする。取消watcherはその間も
		// pending I/Oを解除できるが、その先行pipe Closeを正常終了へ昇格しない。
		cleanup, cancel := context.WithTimeout(context.Background(), limits.CleanupTimeout)
		err := service.Close(cleanup)
		expired := cleanup.Err() != nil
		cancel()
		close(stop)
		<-stopped
		closePipes()
		if err != nil || expired || closeError {
			result = ErrCleanup
		}
		if result == nil && ctx.Err() != nil {
			result = ctx.Err()
		}
	}()
	reader := bufio.NewReaderSize(input, 4096)
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if count == limits.Frames {
			_, err := reader.ReadByte()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return ErrInput
			}
			return ErrLimit
		}
		frame := make([]byte, 0, min(limits.FrameBytes, 4096))
		for {
			value, err := reader.ReadByte()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF && len(frame) == 0 {
				return nil
			}
			if err != nil {
				return ErrInput
			}
			if len(frame) == limits.FrameBytes {
				return ErrLimit
			}
			frame = append(frame, value)
			if value == '\n' {
				break
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err := service.Handle(ctx, frame)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return ErrService
		}
		if len(response) == 0 || len(response) > limits.FrameBytes || response[len(response)-1] != '\n' || bytes.IndexByte(response[:len(response)-1], '\n') >= 0 {
			return ErrOutput
		}
		n, err := output.Write(response)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || n != len(response) {
			return ErrOutput
		}
	}
}
