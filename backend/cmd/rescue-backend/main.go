// rescue-backend は保護bootstrapが未設定の間、製品の制御受付を開始しない。
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
)

func main() {
	// 保護bootstrapの取得/認証経路は未実装。flagやstdinを承認値へ変換しない。
	os.Exit(execute(context.Background(), os.Args[1:], nil, os.Stdin, os.Stdout, os.Stderr))
}

type protectedBootstrap interface {
	ReadApproved(context.Context) (runbackend.ApprovedConfig, error)
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
	config, err := source.ReadApproved(ctx)
	if err != nil || ctx.Err() != nil {
		return refuse()
	}
	runtime, err := runbackend.New(config)
	if runtime != nil {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), config.Limits.CleanupTimeout)
			defer cancel()
			if err := runtime.Close(cleanup); err != nil || cleanup.Err() != nil {
				if code == 0 {
					code = refuse()
				}
			}
		}()
	}
	if err != nil || runtime == nil {
		return refuse()
	}
	if err := runtime.Run(ctx, input, output); err != nil {
		return refuse()
	}
	return 0
}
