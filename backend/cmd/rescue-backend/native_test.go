package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
)

type nativeSourceStub struct {
	t            *testing.T
	config       runbackend.ApprovedConfig
	read, closed int
	readErr      error
}

func (s *nativeSourceStub) ReadApproved(ctx context.Context, args []string, h [3]uintptr) (runbackend.ApprovedConfig, error) {
	s.read++
	if _, ok := ctx.Deadline(); !ok {
		s.t.Error("startup期限がない")
	}
	if len(args) != 10 || args[0] != "--supervised-internal" || h != [3]uintptr{101, 102, 103} {
		s.t.Error("固定引数/標準handleを渡していない")
	}
	return s.config, s.readErr
}
func (*nativeSourceStub) StartupTimeout() time.Duration { return time.Second }
func (*nativeSourceStub) CleanupTimeout() time.Duration { return time.Second }
func (s *nativeSourceStub) Close(context.Context) error { s.closed++; return nil }

type nativeCommandInput struct{ io.ReadCloser }

func (nativeCommandInput) Fd() uintptr { return 101 }

type nativeCommandOutput struct{ *ownedOutput }

func (nativeCommandOutput) Fd() uintptr { return 102 }

type nativeCommandError struct{ bytes.Buffer }

func (*nativeCommandError) Fd() uintptr { return 103 }

func TestNativeCommandEntryRunsRealSessionAfterAuthenticatedSource(t *testing.T) {
	cfg := approvedConfig(t)
	cfg.Backend.MaxTransports = 1
	var frames bytes.Buffer
	for _, m := range []control.Message{
		{Protocol: control.Protocol, Kind: "hello", Generation: cfg.Wire.Generation, Identity: cfg.Wire.Identity, Binding: &cfg.Wire.Binding},
		{Protocol: control.Protocol, Kind: "start", Generation: cfg.Wire.Generation, Identity: cfg.Wire.Identity, Sequence: "1", RequestID: "1", Payload: json.RawMessage(`{"policyRef":"approved-policy"}`)},
		{Protocol: control.Protocol, Kind: "stop", Generation: cfg.Wire.Generation, Identity: cfg.Wire.Identity, Sequence: "2", RequestID: "2", Payload: json.RawMessage(`{}`)},
	} {
		b, e := control.Encode(m)
		if e != nil {
			t.Fatal(e)
		}
		frames.Write(b)
	}
	args, err := bootstrap.FormatLaunchArgs(bootstrap.LaunchValues{IssuerInstanceEpoch: strings.Repeat("c", 64), Wire: cfg.Wire}, 30, [3]uintptr{101, 102, 103})
	if err != nil {
		t.Fatal(err)
	}
	source := &nativeSourceStub{t: t, config: cfg}
	output := nativeCommandOutput{&ownedOutput{}}
	stderr := &nativeCommandError{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code := executeNative(ctx, args, source, nativeCommandInput{io.NopCloser(&frames)}, output, stderr)
	if code != 0 || source.read != 1 || source.closed != 1 || stderr.Len() != 0 || !output.closed {
		t.Fatalf("native受信から本体Sessionへ接続していない: code=%d read=%d close=%d stderr=%q", code, source.read, source.closed, stderr.String())
	}
	if got := len(bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})); got != 3 {
		t.Fatalf("実Session応答数=%d", got)
	}
}

func TestNativeEntryRetiresCallerThreadAfterKeyScopeFatal(t *testing.T) {
	cfg := approvedConfig(t)
	args, err := bootstrap.FormatLaunchArgs(bootstrap.LaunchValues{IssuerInstanceEpoch: strings.Repeat("c", 64), Wire: cfg.Wire}, 30, [3]uintptr{101, 102, 103})
	if err != nil {
		t.Fatal(err)
	}
	source := &nativeSourceStub{t: t, config: cfg, readErr: protectedkey.ErrFatal}
	locked, unlocked := 0, 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output := nativeCommandOutput{&ownedOutput{}}
	stderr := &nativeCommandError{}
	code := executeNativeWithThread(ctx, args, source, nativeCommandInput{io.NopCloser(strings.NewReader(""))}, output, stderr, nativeThreadOperations{func() { locked++ }, func() { unlocked++ }})
	if code != 2 || locked != 1 || unlocked != 0 || source.closed != 1 {
		t.Fatalf("汚染されたkeyscopeのcaller threadを再利用した: code=%d lock=%d unlock=%d close=%d", code, locked, unlocked, source.closed)
	}
}
