package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
)

type cleanupSource struct {
	accepts, closes atomic.Int32
	closeCall       func(context.Context) error
}

func (s *cleanupSource) Accept(ctx context.Context) (net.Conn, error) {
	s.accepts.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *cleanupSource) Close(ctx context.Context) error {
	s.closes.Add(1)
	if s.closeCall != nil {
		return s.closeCall(ctx)
	}
	return nil
}

func TestNativeRejectedRuntimeClosesUntransferredAcceptedSource(t *testing.T) {
	cfg := approvedConfig(t)
	cfg.Limits.CleanupTimeout = -time.Second
	finite, live := false, false
	accepted := &cleanupSource{closeCall: func(ctx context.Context) error {
		_, finite = ctx.Deadline()
		live = ctx.Err() == nil
		return nil
	}}
	cfg.AcceptedSource = accepted
	source := &nativeSourceStub{t: t, config: cfg}
	args, err := bootstrap.FormatLaunchArgs(bootstrap.LaunchValues{IssuerInstanceEpoch: strings.Repeat("c", 64), Wire: cfg.Wire}, 30, [3]uintptr{101, 102, 103})
	if err != nil {
		t.Fatal(err)
	}
	out := nativeCommandOutput{&ownedOutput{}}
	diagnostic := &nativeCommandError{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	code := executeNativeWithThread(ctx, args, source, nativeCommandInput{io.NopCloser(bytes.NewReader(nil))}, out, diagnostic, nativeThreadOperations{func() {}, func() {}})
	if code != 2 || accepted.accepts.Load() != 0 || accepted.closes.Load() != 1 || !finite || !live || out.Len() != 0 {
		t.Fatalf("untransferred source cleanup: code=%d accept=%d close=%d finite=%v live=%v bytes=%d", code, accepted.accepts.Load(), accepted.closes.Load(), finite, live, out.Len())
	}
}

func TestSuccessfulRuntimeTransfersAcceptedSourceWithoutCallerDoubleClose(t *testing.T) {
	cfg := approvedConfig(t)
	accepted := &cleanupSource{}
	cfg.AcceptedSource = accepted
	var input bytes.Buffer
	for _, m := range []control.Message{
		{Protocol: control.Protocol, Kind: "hello", Generation: cfg.Wire.Generation, Identity: cfg.Wire.Identity, Binding: &cfg.Wire.Binding},
		{Protocol: control.Protocol, Kind: "start", Generation: cfg.Wire.Generation, Identity: cfg.Wire.Identity, Sequence: "1", RequestID: "1", Payload: json.RawMessage(`{"policyRef":"approved-policy"}`)},
		{Protocol: control.Protocol, Kind: "stop", Generation: cfg.Wire.Generation, Identity: cfg.Wire.Identity, Sequence: "2", RequestID: "2", Payload: json.RawMessage(`{}`)},
	} {
		frame, err := control.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		input.Write(frame)
	}
	out, diagnostic := &ownedOutput{}, &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code := runApprovedRuntime(ctx, cfg, time.Second, io.NopCloser(&input), out, diagnostic)
	if code != 0 || accepted.closes.Load() != 1 || !out.closed || diagnostic.Len() != 0 {
		t.Fatalf("successful ownership transfer: code=%d close=%d outputClosed=%v diagnosticBytes=%d", code, accepted.closes.Load(), out.closed, diagnostic.Len())
	}
}

func TestPartialRejectedRuntimeAndSourceBothCloseWithIndependentBudget(t *testing.T) {
	cfg := approvedConfig(t)
	finite, live := false, false
	accepted := &cleanupSource{closeCall: func(ctx context.Context) error { _, finite = ctx.Deadline(); live = ctx.Err() == nil; return nil }}
	cfg.AcceptedSource = accepted
	var partial *runbackend.Runtime
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	build := func(c runbackend.ApprovedConfig) (*runbackend.Runtime, error) {
		c.AcceptedSource = nil
		var err error
		partial, err = runbackend.New(c)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		return partial, runbackend.ErrConfig
	}
	out, diagnostic := &ownedOutput{}, &bytes.Buffer{}
	code := runApprovedRuntimeWithConstructor(ctx, cfg, time.Second, io.NopCloser(bytes.NewReader(nil)), out, diagnostic, build)
	if code != 2 || partial == nil || partial.Snapshot().State != "terminated" || accepted.accepts.Load() != 0 || accepted.closes.Load() != 1 || !finite || !live || out.Len() != 0 {
		t.Fatalf("partial construction cleanup: code=%d partial=%v accept=%d close=%d finite=%v live=%v outputBytes=%d", code, partial != nil, accepted.accepts.Load(), accepted.closes.Load(), finite, live, out.Len())
	}
}

func TestUntransferredSourceCleanupFailureNeverReturnsSuccess(t *testing.T) {
	for _, kind := range []string{"error", "deadline", "panic"} {
		t.Run(kind, func(t *testing.T) {
			cfg := approvedConfig(t)
			cfg.Limits.CleanupTimeout = -time.Second
			accepted := &cleanupSource{closeCall: func(ctx context.Context) error {
				switch kind {
				case "deadline":
					<-ctx.Done()
					return ctx.Err()
				case "panic":
					panic("private test failure")
				default:
					return errors.New("private test error")
				}
			}}
			cfg.AcceptedSource = accepted
			out, diagnostic := &ownedOutput{}, &bytes.Buffer{}
			code := runApprovedRuntime(context.Background(), cfg, 20*time.Millisecond, io.NopCloser(bytes.NewReader(nil)), out, diagnostic)
			if code != 2 || accepted.accepts.Load() != 0 || accepted.closes.Load() != 1 || out.Len() != 0 || diagnostic.String() != "製品の制御起動は未接続です\n" {
				t.Fatalf("failure outcome: code=%d accept=%d close=%d output=%d fixedDiagnostic=%v", code, accepted.accepts.Load(), accepted.closes.Load(), out.Len(), diagnostic.String() == "製品の制御起動は未接続です\n")
			}
		})
	}
}
