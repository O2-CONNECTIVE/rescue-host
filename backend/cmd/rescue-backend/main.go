// rescue-backend は保護bootstrapが未設定の間、製品の制御受付を開始しない。
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
)

func main() {
	// 保護bootstrapの取得/認証経路は未実装。flagやstdinを承認値へ変換しない。
	os.Exit(executeNative(context.Background(), os.Args[1:], nil, os.Stdin, os.Stdout, os.Stderr))
}

type protectedBootstrap interface {
	ReadApproved(context.Context) (runbackend.ApprovedConfig, error)
	CleanupTimeout() time.Duration
}

// executeは将来の継承済み保護bootstrapからだけ呼ぶ型付き入口。
// argv/env/hello/UIにはConfig、鍵、grantや解除flagを受け付ける入口を持たせない。
func execute(ctx context.Context, args []string, source protectedBootstrap, input io.ReadCloser, output io.WriteCloser, stderr io.Writer) (code int) {
	refuse := func() int { fmt.Fprintln(stderr, "製品の制御起動は未接続です"); return 2 }
	if len(args) != 1 || args[0] != "--supervised-internal" || source == nil || ctx == nil || ctx.Err() != nil {
		return refuse()
	}
	if _, finite := ctx.Deadline(); !finite {
		return refuse()
	}
	cleanup := source.CleanupTimeout()
	if cleanup <= 0 {
		return refuse()
	}
	config, err := source.ReadApproved(ctx)
	if err != nil || ctx.Err() != nil {
		return refuse()
	}
	return runApprovedRuntime(ctx, config, cleanup, input, output, stderr)
}

// cleanupTimeoutはruntime設定を読む前に保護sourceが固定する。
// Newの拒否では、部分RuntimeがあってもAcceptedSourceはcaller所有のまま。
func runApprovedRuntime(ctx context.Context, config runbackend.ApprovedConfig, cleanupTimeout time.Duration, input io.ReadCloser, output io.WriteCloser, stderr io.Writer) int {
	return runApprovedRuntimeWithConstructor(ctx, config, cleanupTimeout, input, output, stderr, runbackend.New)
}

// 構成の差替えはpackage内試験専用。実入口は常にrunbackend.Newを呼ぶ。
func runApprovedRuntimeWithConstructor(ctx context.Context, config runbackend.ApprovedConfig, cleanupTimeout time.Duration, input io.ReadCloser, output io.WriteCloser, stderr io.Writer, build func(runbackend.ApprovedConfig) (*runbackend.Runtime, error)) (code int) {
	refuse := func() int { fmt.Fprintln(stderr, "製品の制御起動は未接続です"); return 2 }
	untransferred := config.AcceptedSource
	var owned *runbackend.Runtime
	defer func() {
		complete := true
		if owned != nil {
			complete = runtimeCleanup(cleanupTimeout, owned.Close)
		}
		// 先の所有者の失敗でも、独立した未移転sourceの回収を飛ばさない。
		if untransferred != nil {
			closed := runtimeCleanup(cleanupTimeout, untransferred.Close)
			complete = closed && complete
		}
		if !complete && code == 0 {
			code = refuse()
		}
	}()
	if cleanupTimeout <= 0 || build == nil {
		return refuse()
	}
	var err error
	owned, err = build(config)
	if err != nil || owned == nil {
		return refuse()
	}
	// Newの完全成功時だけsourceを移す。以後はSessionだけが所有・回収する。
	untransferred = nil
	if err := owned.Run(ctx, input, output); err != nil {
		return refuse()
	}
	return 0
}

func runtimeCleanup(timeout time.Duration, close func(context.Context) error) (confirmed bool) {
	defer func() {
		if recover() != nil {
			confirmed = false
		}
	}()
	cleanup, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := close(cleanup)
	return err == nil && cleanup.Err() == nil
}
