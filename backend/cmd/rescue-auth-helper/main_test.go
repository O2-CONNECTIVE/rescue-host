package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelperExecutableRejectsUnconfiguredEntryWithoutEchoingInput(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"--operator-auth-internal", "21", "22", "23", strings.Repeat("a", 64)}, {"sensitive-untrusted-input"}} {
		var diagnostic bytes.Buffer
		if code := execute(context.Background(), args, &diagnostic); code == 0 {
			t.Fatal("unconfigured helper executable accepted input")
		}
		if strings.Contains(diagnostic.String(), "sensitive-untrusted-input") {
			t.Fatal("diagnostic echoed input")
		}
	}
}
