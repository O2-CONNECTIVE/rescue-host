package runbackend_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
)

// guest processを作らないmemory pipeのshell端。存在や戻り値を所有証明に使わない。
type runtimeShell struct {
	stdinR, stdoutR, stderrR *io.PipeReader
	stdinW, stdoutW, stderrW *io.PipeWriter
	closes                   atomic.Int32
}

func newRuntimeShell() *runtimeShell {
	s := &runtimeShell{}
	s.stdinR, s.stdinW = io.Pipe()
	s.stdoutR, s.stdoutW = io.Pipe()
	s.stderrR, s.stderrW = io.Pipe()
	return s
}

func (s *runtimeShell) Stdin() io.WriteCloser { return s.stdinW }
func (s *runtimeShell) Stdout() io.Reader     { return s.stdoutR }
func (s *runtimeShell) Stderr() io.Reader     { return s.stderrR }
func (s *runtimeShell) Close(context.Context) error {
	s.closes.Add(1)
	for _, c := range []io.Closer{s.stdinR, s.stdinW, s.stdoutR, s.stdoutW, s.stderrR, s.stderrW} {
		c.Close()
	}
	return nil
}

func approvedFull(t *testing.T) (runbackend.ApprovedConfig, *runtimeShell) {
	t.Helper()
	c := approved(t)
	policy, err := backend.PolicyDigest("full")
	if err != nil {
		t.Fatal(err)
	}
	c.Grant.Binding.Identity.PolicyDigest = policy
	c.Backend.Identity.PolicyDigest = policy
	c.Wire.Identity.PolicyDigest = policy
	c.Backend.OperationMode = "full"
	shell := newRuntimeShell()
	c.InnerShell = shell
	return c, shell
}

func TestApprovedRuntimeRefusesMissingInnerShellWithoutFixedDial(t *testing.T) {
	c, _ := approvedFull(t)
	c.InnerShell = nil
	if r, err := runbackend.New(c); r != nil || !errors.Is(err, runbackend.ErrConfig) {
		t.Fatalf("shell未供給のfullをruntimeにしました: %v", err)
	}
	// restrictedのhelperは未定義で、shellを代わりに受け取らない。拒否時はcaller所有のまま。
	restricted := approved(t)
	shell := newRuntimeShell()
	restricted.InnerShell = shell
	if r, err := runbackend.New(restricted); r != nil || !errors.Is(err, runbackend.ErrConfig) || shell.closes.Load() != 0 {
		t.Fatalf("restrictedへshellを移しました: %v", err)
	}
}

func TestApprovedFullRuntimeClosesTheSameOwnedShellOnStop(t *testing.T) {
	c, shell := approvedFull(t)
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	if shell.closes.Load() != 0 || r.Snapshot().ShellPending != 1 {
		t.Fatal("生成時にshellを閉じたか所有していません")
	}
	input := bytes.Join([][]byte{frame(t, c, "hello", 0, ""), frame(t, c, "start", 1, `{"policyRef":"approved-policy"}`), frame(t, c, "stop", 2, `{}`)}, nil)
	if err := r.Run(context.Background(), io.NopCloser(bytes.NewReader(input)), &output{}); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); s.State != "terminated" || s.ShellPending != 0 || shell.closes.Load() != 1 {
		t.Fatalf("同じruntimeのstopでshellを回収していません: %+v closes=%d", s, shell.closes.Load())
	}
}

func TestApprovedRuntimeLeavesOwnedShellAndSourceWithCallerOnFailedTransfer(t *testing.T) {
	first, shell := approvedFull(t)
	r, err := runbackend.New(first)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	second, _ := approvedFull(t)
	second.InnerShell = shell
	release := make(chan struct{})
	close(release)
	source := &acceptedRuntimeSource{entered: make(chan struct{}, 1), closing: make(chan struct{}), release: release}
	second.AcceptedSource = source
	if other, err := runbackend.New(second); other != nil || !errors.Is(err, runbackend.ErrConfig) {
		t.Fatalf("所有済みshellで別runtimeを作りました: %v", err)
	}
	select {
	case <-source.entered:
		t.Fatal("失敗したruntimeがsourceを消費しました")
	case <-source.closing:
		t.Fatal("失敗したruntimeがcaller所有sourceを閉じました")
	default:
	}
	if shell.closes.Load() != 0 || r.Snapshot().ShellPending != 1 {
		t.Fatal("失敗した移転が元runtimeのshellへ触れました")
	}
}
