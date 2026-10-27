package backend

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"
)

func normalFixtureLifecycle(t *testing.T, mode string) {
	cfg, signer := config(t, mode)
	f := fixture(t)
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		conn, raw, err := fixtureTarget(ctx, f.l.Addr().String())
		if err != nil {
			return nil, nil, err
		}
		return conn, tcpCutoff(raw), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s, cfg)
	cl, err := client(address, cfg, signer, "key")
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ch, err := open(cl, "127.0.0.1", 22)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	echo(t, ch)
	trigger := time.Now()
	s.Revoke(cfg.Identity)
	done := make(chan error, 1)
	go func() { done <- cl.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SSH終端なし")
		}
	case <-time.After(time.Second):
		t.Fatal("SSH終端1秒超過")
	}
	if n, err := ch.Write([]byte("fresh-after-revoke")); n != 0 || err == nil {
		t.Fatal("失効後の新規SSH送信")
	}
	select {
	case event := <-f.events:
		if e, ok := event.err.(net.Error); ok && e.Timeout() {
			t.Fatal("timeoutを終端と誤認")
		}
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("peer終端1秒超過")
	}
	closeSession(t, s)
	if snap := s.Snapshot(); snap.Owned != 0 || snap.Pending != 0 || snap.Active != 0 {
		t.Fatal("終了後に参照残留")
	}
	t.Logf("normal mode=%s peer_and_ssh_end_ms=%d owners=0", mode, time.Since(trigger).Milliseconds())
}

func TestAFKUnit4BoundedLifecycleRepetition(t *testing.T) {
	runtime.GC()
	baseline := runtime.NumGoroutine()
	check := func(label string) {
		t.Helper()
		deadline := time.Now().Add(250 * time.Millisecond)
		for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		current := runtime.NumGoroutine()
		network.Lock()
		listeners, conns := len(network.listeners), len(network.conns)
		network.Unlock()
		if current > baseline || listeners != 0 || conns != 0 {
			t.Fatalf("反復後残留 %s goroutine=%d baseline=%d listeners=%d conns=%d", label, current, baseline, listeners, conns)
		}
		t.Logf("quiescent checkpoint=%s goroutines=%d baseline=%d listeners=0 connections=0", label, current, baseline)
	}
	for n := 0; n < 3; n++ {
		mode := "restricted"
		if n%2 == 1 {
			mode = "full"
		}
		label := fmt.Sprintf("normal-%d", n)
		t.Run(label, func(t *testing.T) { normalFixtureLifecycle(t, mode) })
		check(label)
		label = fmt.Sprintf("single-%d", n)
		t.Run(label, TestIndependentTCPCutoff)
		check(label)
		label = fmt.Sprintf("double-%d", n)
		t.Run(label, TestCloseFailureRetainsAndRevokes)
		check(label)
		if faultEvidence == nil || faultEvidence["target_tcp_live_after_close_budget"] != true || faultEvidence["real_os_close_failure_tested"] != false {
			t.Fatal("二重故障未達の証拠を失った")
		}
		t.Logf("double_fault round=%d physical_cutoff=NOT_MET canary_ms=%v", n, faultEvidence["physical_canary_ms_after_revoke"])
	}
	t.Run("owned-child", TestOwnedProcessExitClosesTCP)
	check("owned-child")
}
