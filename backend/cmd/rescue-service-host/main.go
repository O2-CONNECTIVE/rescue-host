// rescue-service-host は固定保護構成だけを使うSCM専用入口である。
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceprovision"
)

func main() { os.Exit(execute(context.Background(), os.Args[1:], os.Stderr)) }

func execute(ctx context.Context, args []string, diagnostic io.Writer) int {
	// service登録/console fallback/config pathの引数は提供しない。
	if len(args) == 0 && ctx != nil && ctx.Err() == nil {
		if err := serviceprovision.RunFixed(ctx); err == nil {
			return 0
		}
	}
	if diagnostic != nil {
		fmt.Fprintln(diagnostic, "保護serviceの固定構成は未接続です")
	}
	return 2
}
