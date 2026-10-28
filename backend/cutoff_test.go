package backend

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 試験専用。raw は fixture への実 TCP であり、製品宛先の差替え設定ではない。
func fixtureTarget(ctx context.Context, address string) (net.Conn, *net.TCPConn, error) {
	conn, err := testDial(ctx, address)
	if err != nil {
		return nil, nil, err
	}
	raw := conn.(*trackedConn).Conn.(*net.TCPConn)
	conn.(*trackedConn).Conn = &tcpStream{raw}
	return conn, raw, nil
}

var cutoffEvidence []map[string]any

func TestIndependentTCPCutoff(t *testing.T) {
	for _, mode := range []string{"adapter-error", "adapter-block", "expiry-adapter-block"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			cfg, signer := config(t, "restricted")
			if strings.HasPrefix(mode, "expiry") {
				cfg.LeaseDuration = 350 * time.Millisecond
			}
			block := make(chan struct{})
			var fault *failedClose
			var raw *net.TCPConn
			// OpenChannel 完了を介して公開し、試験の後処理より先に遅延取得しない。
			targetReady := make(chan struct{})
			s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
				conn, tcp, err := fixtureTarget(ctx, f.l.Addr().String())
				if err != nil {
					return nil, nil, err
				}
				raw = tcp
				fault = &failedClose{Conn: conn}
				if mode == "adapter-error" {
					fault.fail.Store(true)
				} else {
					fault.block = block
				}
				close(targetReady)
				return fault, tcpCutoff(tcp), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			address := start(t, s, cfg)
			t.Cleanup(func() {
				onceRelease(block)
				if fault != nil {
					fault.fail.Store(false)
				}
				closeSession(t, s)
			})
			cl, err := client(address, cfg, signer, "key")
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			channel, err := open(cl, "127.0.0.1", 22)
			if err != nil {
				t.Fatal(err)
			}
			<-targetReady
			echo(t, channel)
			trigger := time.Now()
			if strings.HasPrefix(mode, "expiry") {
				// echo 直後を起点とし、TTL 到達前の待ちも1秒に含める。
				for s.Snapshot().State == "active" && time.Since(trigger) < time.Second {
					time.Sleep(time.Millisecond)
				}
				if s.Snapshot().State != "revoking" {
					t.Fatal("期限切れで失効しない")
				}
				// TTL 到達より前の起点なので、この1秒判定は保守的に厳しい。
			} else if mode == "adapter-error" {
				channel.Close()
			} else if err := s.Revoke(cfg.Identity); err != nil {
				t.Fatal(err)
			}
			// 明示失効起点から1秒以内。expiry はここより早い既知の trigger 起点で測る。
			deadline := trigger.Add(time.Second)
			sshDone := make(chan error, 1)
			go func() { sshDone <- cl.Wait() }()
			select {
			case err := <-sshDone:
				if err == nil {
					t.Fatal("SSH切断がない")
				}
			case <-time.After(time.Until(deadline)):
				t.Fatal("SSH切断が1秒超過")
			}
			sshMS := time.Since(trigger).Milliseconds()
			if n, err := channel.Write([]byte("new-ssh-data-after-revoke")); n != 0 || err == nil {
				t.Fatal("SSH channelに新規データを書き込めた")
			}
			var sshByte [1]byte
			if n, err := channel.Read(sshByte[:]); n != 0 || err == nil {
				t.Fatal("SSH channelで新規データを受信")
			}
			if s.Snapshot().State != "revoking" {
				t.Fatal("認可が失効していない")
			}
			authMS := time.Since(trigger).Milliseconds()
			select {
			case event := <-f.events:
				if n, ok := event.err.(net.Error); ok && n.Timeout() {
					t.Fatal("期限切れをEOFと誤認")
				}
			case <-time.After(time.Until(deadline)):
				t.Fatal("転送先peer EOFが1秒超過")
			}
			peerMS := time.Since(trigger).Milliseconds()
			if time.Now().After(deadline) {
				t.Fatal("切断期限超過")
			}
			// 実 raw の両方向で失効後の新規データを観測。Write成功だけで切断を判定しない。
			raw.SetDeadline(deadline)
			if n, err := raw.Write([]byte("new-after-revoke")); n != 0 || !errors.Is(err, net.ErrClosed) {
				t.Fatalf("閉鎖後の送信: %d %v", n, err)
			}
			b := make([]byte, 32)
			if n, err := raw.Read(b); n != 0 || !errors.Is(err, net.ErrClosed) {
				t.Fatalf("閉鎖後の受信: %d %v", n, err)
			}
			if s.Renew(cfg.Identity, 999) == nil {
				t.Fatal("失効後の更新")
			}
			next, err := client(address, cfg, signer, "key")
			if next != nil {
				next.Close()
			}
			if err == nil {
				t.Fatal("失効後の新認証")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			err = s.Close(ctx)
			cancel()
			snap := s.Snapshot()
			if err == nil || snap.State != "revoking" || snap.Owned == 0 || snap.CutoffPending != 0 {
				t.Fatalf("物理切断とアダプター終了を混同: %v %+v", err, snap)
			}
			if fault.calls.Load() != 1 && mode != "adapter-error" {
				t.Fatal("停止中closeが増殖")
			}
			fault.fail.Store(false)
			onceRelease(block)
			closeSession(t, s)
			if s.Snapshot().Owned != 0 {
				t.Fatal("回復後に参照残留")
			}
			cutoffEvidence = append(cutoffEvidence, map[string]any{"fault": mode, "ssh_closed_ms": sshMS, "target_peer_read_ended_ms": peerMS, "raw_new_write_rejected": true, "raw_new_read_rejected": true, "authorization_rejected": true, "authorization_observed_ms": authMS, "ssh_channel_new_data_rejected": true, "measurement_origin": "echo直後。expiryケースは期限到達前の待ち込み", "adapter_retained_until_recovery": true, "ownership_after_recovery": 0, "physical_tcp_cutoff_within_1s": true, "os_close_fault_injected": false})
		})
	}
}

// 製品と同じ raw 本人の二経路 close が、順序にかかわらず終了することを確認する。
func TestRawTCPStopRace(t *testing.T) {
	for n := 0; n < 32; n++ {
		f := fixture(t)
		cfg, _ := config(t, "restricted")
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		conn, raw, err := fixtureTarget(context.Background(), f.l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.(*trackedConn).Conn = &tcpStream{raw}
		s.mu.Lock()
		h := s.ownLocked(conn)
		h.cutoff = tcpCutoff(raw)
		s.mu.Unlock()
		closeSession(t, s)
		snap := s.Snapshot()
		if snap.Owned != 0 || snap.CloseFailures != 0 || snap.CutoffFailures != 0 {
			t.Fatalf("実TCPの既閉を故障扱い: %+v", snap)
		}
	}
}

func TestIndependentCutoffBlockedRetainsSingleAttempt(t *testing.T) {
	cfg, _ := config(t, "restricted")
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer right.Close()
	block := make(chan struct{})
	var calls atomic.Int32
	s.mu.Lock()
	h := s.ownLocked(left)
	h.cutoff = func() error { calls.Add(1); <-block; return nil }
	s.mu.Unlock()
	t.Cleanup(func() { onceRelease(block); closeSession(t, s) })
	for n := 0; n < 8; n++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		if s.Close(ctx) == nil {
			t.Fatal("独立停止中を完了扱い")
		}
		cancel()
	}
	snap := s.Snapshot()
	if calls.Load() != 1 || snap.State != "revoking" || snap.Owned != 1 || snap.CutoffPending != 1 {
		t.Fatalf("停止中参照/上限: %+v calls=%d", snap, calls.Load())
	}
	onceRelease(block)
	closeSession(t, s)
}

// TCP 終端と fixture 内の独立処理の寿命は別であることを実測する。
func TestTargetEOFDoesNotCancelFixtureWork(t *testing.T) {
	l := testListen(t)
	ctx, cancel := context.WithCancel(context.Background())
	var ticks atomic.Int64
	workDone := make(chan struct{})
	peerDone := make(chan error, 1)
	go func() {
		defer close(workDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		ttl := time.NewTimer(2 * time.Second)
		defer ttl.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ttl.C:
				return
			case <-ticker.C:
				ticks.Add(1)
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-workDone })
	go func() {
		raw, err := l.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = io.Copy(raw, raw)
		peerDone <- err
	}()
	cfg, signer := config(t, "restricted")
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		conn, raw, err := fixtureTarget(ctx, l.Addr().String())
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
	channel, err := open(cl, "127.0.0.1", 22)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, channel)
	trigger := time.Now()
	closeSession(t, s)
	select {
	case err := <-peerDone:
		if n, ok := err.(net.Error); ok && n.Timeout() {
			t.Fatal("timeoutをEOFと誤認")
		}
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("peer EOFが1秒超過")
	}
	peerMS := time.Since(trigger).Milliseconds()
	before := ticks.Load()
	time.Sleep(30 * time.Millisecond)
	after := ticks.Load()
	if after <= before {
		t.Fatal("独立fixture処理の寿命観測ができない")
	}
	cancel()
	<-workDone
	cutoffEvidence = append(cutoffEvidence, map[string]any{"fault": "independent-fixture-work", "target_peer_read_ended_ms": peerMS, "work_ticks_after_tcp_eof": after - before, "fixture_work_stopped_by_own_cancel": true, "real_sshd_children_tested": false})
}

func TestLateTargetCutoffAfterRevocation(t *testing.T) {
	f := fixture(t)
	cfg, signer := config(t, "restricted")
	entered, release, block := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var fault *failedClose
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		close(entered)
		<-release
		conn, raw, err := fixtureTarget(context.Background(), f.l.Addr().String())
		if err != nil {
			return nil, nil, err
		}
		fault = &failedClose{Conn: conn, block: block}
		return fault, tcpCutoff(raw), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s, cfg)
	t.Cleanup(func() { onceRelease(release); onceRelease(block); closeSession(t, s) })
	cl, err := client(address, cfg, signer, "key")
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	opened := make(chan error, 1)
	go func() {
		ch, err := open(cl, "127.0.0.1", 22)
		if ch != nil {
			ch.Close()
		}
		opened <- err
	}()
	<-entered
	if s.Revoke(cfg.Identity) != nil {
		t.Fatal("失効失敗")
	}
	trigger := time.Now()
	close(release)
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("失効後の遅延dialを公開")
		}
	case <-time.After(time.Second):
		t.Fatal("拒否が1秒超過")
	}
	select {
	case event := <-f.events:
		if n, ok := event.err.(net.Error); ok && n.Timeout() {
			t.Fatal("timeoutをEOFと誤認")
		}
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("遅延dialのpeer EOFが1秒超過")
	}
	// dial worker の完了を Close で待ち、故障adapterだけの参照を残す。
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = s.Close(waitCtx)
	waitCancel()
	if err == nil || s.Snapshot().Owned == 0 || s.Snapshot().CutoffPending != 0 {
		t.Fatal("遅延dialの停止参照が不正")
	}
	onceRelease(block)
	closeSession(t, s)
	cutoffEvidence = append(cutoffEvidence, map[string]any{"fault": "late-dial-after-revocation", "published": false, "target_peer_read_ended_within_1s": true, "recovered_owners": s.Snapshot().Owned})
}

// OS のソケット終了とアダプター故障を分けるための、所有子プロセスだけの試験。
// 製品 supervisor、Job Object、既存プロセス停止の実装ではない。
func TestOwnedProcessExitClosesTCP(t *testing.T) {
	f := fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), "RESCUE_OWNED_SOCKET_CHILD=1", "RESCUE_OWNED_SOCKET_ADDRESS="+f.l.Addr().String())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("所有子終了未確認")
			}
		}
	})
	ready := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- text }()
	select {
	case line := <-ready:
		if line != "READY\n" {
			t.Fatalf("子準備: %q %s", line, stderr.String())
		}
	case <-time.After(time.Second):
		t.Fatal("子準備超過")
	}
	trigger := time.Now()
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		exited = true
		if err == nil {
			t.Fatal("強制停止の証拠なし")
		}
	case <-time.After(time.Second):
		t.Fatal("子終了が1秒超過")
	}
	processMS := time.Since(trigger).Milliseconds()
	select {
	case event := <-f.events:
		if n, ok := event.err.(net.Error); ok && n.Timeout() {
			t.Fatal("timeoutをEOFと誤認")
		}
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("子所有TCP EOFが1秒超過")
	}
	if time.Since(trigger) >= time.Second {
		t.Fatal("物理切断が1秒超過")
	}
	cutoffEvidence = append(cutoffEvidence, map[string]any{"fault": "owned-child-process-kill", "child_pid": cmd.Process.Pid, "child_wait_confirmed": true, "process_exit_ms": processMS, "target_peer_read_ended_ms": time.Since(trigger).Milliseconds(), "protected_supervisor_implemented": false, "arbitrary_inner_process_lifetime": "未検証・保証なし"})
}

func ownedSocketChild() int {
	address := os.Getenv("RESCUE_OWNED_SOCKET_ADDRESS")
	host, port, err := net.SplitHostPort(address)
	p, pe := strconv.Atoi(port)
	if err != nil || pe != nil || host != "127.0.0.1" || p <= 1024 || p > 65535 {
		return 2
	}
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		return 3
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	text := []byte("owned-process-canary")
	if _, err = conn.Write(text); err != nil {
		return 4
	}
	b := make([]byte, len(text))
	if _, err = io.ReadFull(conn, b); err != nil {
		return 5
	}
	fmt.Println("READY")
	_, err = conn.Read(b)
	if err != nil {
		return 0
	}
	return 6
}
