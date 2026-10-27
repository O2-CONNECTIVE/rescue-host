package backend

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

func noTargetSession(t *testing.T, cfg Config, dials *atomic.Int64) *Session {
	t.Helper()
	s, err := newSession(cfg, func(context.Context) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("認証のみ。target禁止")
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestNextN3IdleBudgetAndIsolation(t *testing.T) {
	a, signerA := config(t, "restricted")
	b, signerB := config(t, "full")
	a.MaxTransports = 1
	b.MaxTransports = 1
	var dials atomic.Int64
	sA, sB := noTargetSession(t, a, &dials), noTargetSession(t, b, &dials)
	addressA, addressB := start(t, sA, a), start(t, sB, b)
	first, err := client(addressA, a, signerA, "key")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	awaitSnapshot(t, sA, func(x Snapshot) bool { return x.Handshakes == 0 && x.Transports == 1 })
	refused, err := client(addressA, a, signerA, "key")
	if refused != nil {
		refused.Close()
	}
	if err == nil {
		t.Fatal("認証済idle枠を越えて受理")
	}
	other, err := client(addressB, b, signerB, "key")
	if err != nil {
		t.Fatal("別sessionへ飽和が波及")
	}
	defer other.Close()
	first.Close()
	awaitSnapshot(t, sA, func(x Snapshot) bool { return x.Transports == 0 })
	reused, err := client(addressA, a, signerA, "key")
	if err != nil {
		t.Fatal("正常close後のtransport枠再利用不可")
	}
	defer reused.Close()
	sA.Revoke(a.Identity)
	awaitSnapshot(t, sA, func(x Snapshot) bool { return x.Transports == 0 })
	if sB.Snapshot().State != "active" || sB.Snapshot().Transports != 1 {
		t.Fatal("A失効がBへ波及")
	}
	if dials.Load() != 0 {
		t.Fatal("idle認証からtarget接続")
	}
}
func TestNextN3ConcurrentAdmission(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			cfg, signer := config(t, mode)
			cfg.MaxTransports = 1
			cfg.MaxHandshakes = 2
			var dials atomic.Int64
			s := noTargetSession(t, cfg, &dials)
			address := start(t, s, cfg)
			barrier := make(chan struct{})
			type result struct {
				cl  *ssh.Client
				err error
			}
			results := make(chan result, 2)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-barrier
					cl, err := client(address, cfg, signer, "key")
					results <- result{cl, err}
				}()
			}
			close(barrier)
			wg.Wait()
			close(results)
			accepted := 0
			var held []*ssh.Client
			for result := range results {
				if result.err == nil {
					accepted++
					held = append(held, result.cl)
				} else if result.cl != nil {
					result.cl.Close()
				}
			}
			defer func() {
				for _, cl := range held {
					cl.Close()
				}
			}()
			if accepted != 1 {
				t.Fatal("並行admissionで予算回避")
			}
			awaitSnapshot(t, s, func(x Snapshot) bool { return x.Transports == 1 && x.Handshakes == 0 })
			if dials.Load() != 0 {
				t.Fatal("認証だけでtarget接続")
			}
		})
	}
}
func TestNextN3ClosingOwnerKeepsBudget(t *testing.T) {
	cfg, signer := config(t, "restricted")
	cfg.MaxTransports = 1
	var dials atomic.Int64
	s := noTargetSession(t, cfg, &dials)
	left, right := net.Pipe()
	defer right.Close()
	block := make(chan struct{})
	// 停止中adapterの所有は注入モデル。admissionは以下で実SSHを使う。
	s.mu.Lock()
	h := s.ownLocked(&failedClose{Conn: left, block: block})
	h.transport = true
	s.transports++
	s.closeOneLocked(h)
	s.mu.Unlock()
	address := start(t, s, cfg)
	t.Cleanup(func() { onceRelease(block) })
	cl, err := client(address, cfg, signer, "key")
	if cl != nil {
		cl.Close()
	}
	if err == nil {
		t.Fatal("close未完了の枠を再利用")
	}
	snap := s.Snapshot()
	if snap.Transports != 1 || snap.Owned != 1 || snap.Closing != 1 {
		t.Fatal("停止中の所有参照を失った")
	}
	close(block)
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Transports == 0 && x.Owned == 0 })
	valid, err := client(address, cfg, signer, "key")
	if err != nil {
		t.Fatal("復旧後の枠再利用失敗")
	}
	valid.Close()
	if dials.Load() != 0 {
		t.Fatal("認証からtarget接続")
	}
}
func TestNextN3ConfigBounds(t *testing.T) {
	for _, which := range []string{"default", "maximum", "negative", "high"} {
		t.Run(which, func(t *testing.T) {
			cfg, _ := config(t, "restricted")
			switch which {
			case "maximum":
				cfg.MaxTransports = 64
			case "negative":
				cfg.MaxTransports = -1
			case "high":
				cfg.MaxTransports = 65
			}
			s, err := New(cfg)
			if which == "default" || which == "maximum" {
				if err != nil {
					t.Fatal(err)
				}
				defer closeSession(t, s)
				expected := 8
				if which == "maximum" {
					expected = 64
				}
				if s.maxTransports != expected {
					t.Fatal("既定上限が不正")
				}
				return
			}
			if err == nil {
				closeSession(t, s)
				t.Fatal("範囲外上限を許可")
			}
		})
	}
}
