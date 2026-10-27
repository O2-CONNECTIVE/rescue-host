package backend

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// adapter停止の待ちとTCP半閉鎖を独立に注入する試験専用wrapper。
type halfCloseFault struct{ *failedClose }

func (c *halfCloseFault) CloseWrite() error {
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}

func TestCloseoutN12ReservationHeldUntilOwnerReleased(t *testing.T) {
	f := fixture(t)
	cfg, signer := config(t, "restricted")
	cfg.MaxChannels = 1
	block := make(chan struct{})
	var calls atomic.Int32
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		conn, raw, e := fixtureTarget(ctx, f.l.Addr().String())
		if e != nil {
			return nil, nil, e
		}
		calls.Add(1)
		return &halfCloseFault{&failedClose{Conn: conn, block: block}}, tcpCutoff(raw), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s, cfg)
	t.Cleanup(func() { onceRelease(block); closeSession(t, s) })
	cl, err := client(address, cfg, signer, "key")
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ch, err := open(cl, "127.0.0.1", 22)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, ch)
	if err = ch.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { _, e := io.Copy(io.Discard, ch); ended <- e }()
	select {
	case err = <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("正常EOF未完了")
	}
	awaitSnapshot(t, s, func(v Snapshot) bool { return v.Closing == 1 && v.CutoffPending == 0 })
	if v := s.Snapshot(); v.State != "active" || v.Active != 1 || v.Owned != 2 {
		t.Fatalf("終了未確認targetの容量を喪失: %+v", v)
	}
	other, e := open(cl, "127.0.0.1", 22)
	if other != nil {
		other.Close()
	}
	if e == nil || calls.Load() != 1 {
		t.Fatal("target終了待ち中に上限を超えてdial")
	}
	onceRelease(block)
	awaitSnapshot(t, s, func(v Snapshot) bool { return v.Active == 0 && v.Owned == 1 })
	again, err := open(cl, "127.0.0.1", 22)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	echo(t, again)
	if calls.Load() != 2 {
		t.Fatal("回収後の枠再利用が不正")
	}
	t.Log("未確認owner中はActive=1を保持、解放後に再利用。OS close故障の注入なし")
}
