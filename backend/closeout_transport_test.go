package backend

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type transportProbe struct {
	raw   *net.TCPConn
	fault *failedClose
}

// 停止故障はadapterだけへ注入する。OSのclose失敗としては扱わない。
func TestCloseoutN9TransportAdapterCutoff(t *testing.T) {
	for _, mode := range []string{"error", "block"} {
		t.Run(mode, func(t *testing.T) {
			cfg, signer := config(t, "restricted")
			cfg.MaxTransports = 1
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Ready(cfg.Identity); err != nil {
				t.Fatal(err)
			}
			if err = s.Activate(cfg.Identity); err != nil {
				t.Fatal(err)
			}
			l := testListen(t)
			block := make(chan struct{})
			ready := make(chan transportProbe, 8)
			var wg sync.WaitGroup
			var mu sync.Mutex
			var records []net.Conn
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					conn, err := l.Accept()
					if err != nil {
						return
					}
					raw := conn.(*net.TCPConn)
					record := track(&tcpStream{raw})
					mu.Lock()
					records = append(records, record)
					mu.Unlock()
					fault := &failedClose{Conn: record}
					if mode == "error" {
						fault.fail.Store(true)
					} else {
						fault.block = block
					}
					ready <- transportProbe{raw, fault}
					wg.Add(1)
					go func() {
						defer wg.Done()
						if errors.Is(s.serve(fault, tcpCutoff(raw)), ErrNotAccepted) {
							record.Close()
						}
					}()
				}
			}()
			t.Cleanup(func() {
				l.Close()
				onceRelease(block)
				for len(ready) > 0 {
					(<-ready).fault.fail.Store(false)
				}
				closeSession(t, s)
				mu.Lock()
				for _, c := range records {
					c.Close()
				}
				mu.Unlock()
				wg.Wait()
			})
			cl, err := client(l.Addr().String(), cfg, signer, "key")
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			p := <-ready
			t.Cleanup(func() { p.fault.fail.Store(false); onceRelease(block) })
			if snap := s.Snapshot(); snap.Transports != 1 || snap.CutoffPending != 1 {
				t.Fatalf("停止参照不足: %+v", snap)
			}
			trigger := time.Now()
			deadline := trigger.Add(time.Second)
			if err = s.Revoke(cfg.Identity); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cl.Wait() }()
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("SSH切断なし")
				}
			case <-time.After(time.Until(deadline)):
				t.Fatal("SSH切断が1秒超過")
			}
			if _, _, err = cl.SendRequest("fresh-after-revoke", true, []byte("nonce")); err == nil {
				t.Fatal("失効後にSSH要求が成立")
			}
			if n, e := p.raw.Write([]byte("fresh-raw-write")); n != 0 || !errors.Is(e, net.ErrClosed) {
				t.Fatalf("raw送信残存: %d %v", n, e)
			}
			var data [1]byte
			if n, e := p.raw.Read(data[:]); n != 0 || !errors.Is(e, net.ErrClosed) {
				t.Fatalf("raw受信残存: %d %v", n, e)
			}
			if time.Now().After(deadline) {
				t.Fatal("両方向確認が1秒超過")
			}
			if s.Renew(cfg.Identity, 999) == nil {
				t.Fatal("失効後更新")
			}
			next, e := client(l.Addr().String(), cfg, signer, "key")
			if next != nil {
				next.Close()
			}
			if e == nil {
				t.Fatal("失効後認証")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			e = s.Close(ctx)
			cancel()
			snap := s.Snapshot()
			if e == nil || snap.State != "revoking" || snap.Transports != 1 || snap.Owned != 1 || snap.CutoffPending != 0 {
				t.Fatalf("終了未確認参照を喪失: %v %+v", e, snap)
			}
			if mode == "block" && p.fault.calls.Load() != 1 {
				t.Fatal("blocked Close試行が増殖")
			}
			p.fault.fail.Store(false)
			onceRelease(block)
			closeSession(t, s)
			if snap = s.Snapshot(); snap.Transports != 0 || snap.Owned != 0 {
				t.Fatalf("復旧後残留: %+v", snap)
			}
			t.Logf("adapter_fault=%s all_checks_and_recovery_ms=%d owner_retained_until_recovery=true os_close_fault=false", mode, time.Since(trigger).Milliseconds())
		})
	}
}

// 公開入口には実TCP本人を渡す。記録wrapperは終了確認だけを担う。
func TestCloseoutN9PublicRawTransport(t *testing.T) {
	cfg, signer := config(t, "restricted")
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ready(cfg.Identity); err != nil {
		t.Fatal(err)
	}
	if err = s.Activate(cfg.Identity); err != nil {
		t.Fatal(err)
	}
	l := testListen(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		raw, err := l.Accept()
		if err != nil {
			return
		}
		record := track(&tcpStream{raw.(*net.TCPConn)})
		defer record.Close()
		if err := s.Serve(raw); errors.Is(err, ErrNotAccepted) {
			record.Close()
		}
	}()
	t.Cleanup(func() { l.Close(); closeSession(t, s); wg.Wait() })
	cl, err := client(l.Addr().String(), cfg, signer, "key")
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if snap := s.Snapshot(); snap.Transports != 1 || snap.CutoffPending != 1 {
		t.Fatalf("公開raw入口で独立停止参照がない: %+v", snap)
	}
	trigger := time.Now()
	if err := s.Revoke(cfg.Identity); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cl.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SSH終端なし")
		}
	case <-time.After(time.Second):
		t.Fatal("SSH終端が1秒超過")
	}
	ctx, cancel := context.WithDeadline(context.Background(), trigger.Add(time.Second))
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.Owned != 0 || snap.Transports != 0 {
		t.Fatalf("参照残留: %+v", snap)
	}
}
