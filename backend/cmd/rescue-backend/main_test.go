package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestClosedCommandProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "rescue-backend.exe")
	if _, err := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", path, ".").CombinedOutput(); err != nil {
		t.Fatalf("fixed command build failed: %v", err)
	}
	for index, args := range [][]string{nil, {"--stdio"}, {"--supervised-internal"}, {"--start", "SECRET_TEST_ARGUMENT"}} {
		childCtx, childCancel := context.WithTimeout(context.Background(), 2*time.Second)
		command := exec.CommandContext(childCtx, path, args...)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 || childCtx.Err() != nil || stdout.Len() != 0 || stderr.String() != "製品の制御起動は未接続です\n" {
			code := -1
			if ok {
				code = exit.ExitCode()
			}
			t.Errorf("closed command case %d: exit=%d timeout=%v stdoutBytes=%d fixedStderr=%v", index, code, childCtx.Err() != nil, stdout.Len(), stderr.String() == "製品の制御起動は未接続です\n")
		}
		childCancel()
	}
}
