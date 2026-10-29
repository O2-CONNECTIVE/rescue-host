package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
)

// protectedNativeBootstrap は保護された配置元で構成するnative receiverです。
// 外部入力やargvから、この実装/配置profileを選ぶことはできません。
type protectedNativeBootstrap interface {
	ReadApproved(context.Context, []string, [3]uintptr) (runbackend.ApprovedConfig, error)
	StartupTimeout() time.Duration
	CleanupTimeout() time.Duration
	Close(context.Context) error
}

type nativeThreadOperations struct{ lock, unlock func() }

func executeNative(ctx context.Context, args []string, source protectedNativeBootstrap, input io.ReadCloser, output io.WriteCloser, stderr io.Writer) (code int) {
	return executeNativeWithThread(ctx, args, source, input, output, stderr, nativeThreadOperations{runtime.LockOSThread, runtime.UnlockOSThread})
}

func executeNativeWithThread(ctx context.Context, args []string, source protectedNativeBootstrap, input io.ReadCloser, output io.WriteCloser, stderr io.Writer, thread nativeThreadOperations) (code int) {
	refuse := func() int { fmt.Fprintln(stderr, "製品の制御起動は未接続です"); return 2 }
	if source == nil || ctx == nil || ctx.Err() != nil {
		return refuse()
	}
	startup, cleanup := source.StartupTimeout(), source.CleanupTimeout()
	if startup <= 0 || cleanup <= 0 {
		return refuse()
	}
	values := [3]uintptr{}
	for i, v := range []any{input, output, stderr} {
		file, ok := v.(interface{ Fd() uintptr })
		if !ok {
			return refuse()
		}
		values[i] = file.Fd()
	}
	// 元のcaller threadを固定し、impersonating callerをclean workerへ移しません。
	thread.lock()
	poisoned := false
	defer func() {
		if !poisoned {
			thread.unlock()
		}
	}()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), cleanup)
		defer cancel()
		err := source.Close(closeCtx)
		if fatalNativeThread(err) {
			poisoned = true
		}
		if err != nil && code == 0 {
			code = refuse()
		}
	}()
	readCtx, cancel := context.WithTimeout(ctx, startup)
	config, err := source.ReadApproved(readCtx, append([]string(nil), args...), values)
	startupErr := readCtx.Err()
	cancel()
	if fatalNativeThread(err) {
		poisoned = true
	}
	if err != nil || startupErr != nil || ctx.Err() != nil {
		return refuse()
	}
	// startup期限をセッション寿命へ流用せず、runtimeが元の絶対hard deadlineを適用します。
	return runApprovedRuntime(ctx, config, cleanup, input, output, stderr)
}

func fatalNativeThread(err error) bool {
	return errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal)
}
