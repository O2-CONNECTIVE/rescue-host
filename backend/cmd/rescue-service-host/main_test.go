package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestFixedServiceEntryRejectsUnconfiguredAndOverrideArguments(t *testing.T) {
	// 合成のprocess環境だけを変更し、信頼設定の補完に使わないことを確認する。
	t.Setenv("RESCUE_SERVICE_CONFIG", `C:\synthetic\record.json`)
	t.Setenv("RESCUE_SERVICE_TOKEN", "123")
	t.Setenv("RESCUE_SERVICE_SID", "S-1-5-18")
	for _, args := range [][]string{nil, {"--install"}, {"--config", `C:\\untrusted\\record.json`}, {"--supervised-internal"}, {"--debug"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var output bytes.Buffer
			if code := execute(context.Background(), args, &output); code != 2 {
				t.Fatalf("unconfigured/override entry returned %d", code)
			}
			if output.String() != "保護serviceの固定構成は未接続です\n" {
				t.Fatalf("unexpected public diagnostic: %q", output.String())
			}
		})
	}
}
