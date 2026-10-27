package backend

import (
	"bytes"
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// SSH/targetの両Close経路とtarget半閉鎖を故障させる注入。OS故障ではない。
type heldStop struct {
	net.Conn
	fail *atomic.Bool
}

func (c *heldStop) Close() error {
	if c.fail.Load() {
		return errors.New("自己fixtureの停止故障")
	}
	return c.Conn.Close()
}
func (c *heldStop) CloseWrite() error {
	if c.fail.Load() {
		return errors.New("自己fixtureの半閉鎖故障")
	}
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}
func heldCutoff(raw *net.TCPConn, fail *atomic.Bool) func() error {
	return func() error {
		if fail.Load() {
			return errors.New("自己fixtureの独立停止故障")
		}
		return tcpCutoff(raw)()
	}
}

type peerReads struct{ out chan []byte }

func (w peerReads) Write(p []byte) (int, error) {
	w.out <- append([]byte(nil), p...)
	return len(p), nil
}

func assertPeerBytes(t *testing.T, observed <-chan []byte, expected []byte) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	var got []byte
	for len(got) < len(expected) {
		select {
		case p := <-observed:
			got = append(got, p...)
		case <-deadline.C:
			t.Fatal("peerの全量観測なし")
		}
	}
	if !bytes.Equal(got, expected) {
		t.Fatalf("peer観測不一致: %q", got)
	}
}

func assertChannelEndedWithoutData(t *testing.T, ch ssh.Channel) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() { var p [128]byte; n, e := ch.Read(p[:]); done <- result{n, e} }()
	select {
	case r := <-done:
		if r.n != 0 || r.err == nil {
			t.Fatalf("失効後データまたは終了未確認: %d %v", r.n, r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH channelの拒否未確認")
	}
}

func TestCloseoutN10PostRevokeFreshData(t *testing.T) {
	for _, direction := range []string{"client-to-target", "target-to-client"} {
		t.Run(direction, func(t *testing.T) {
			observed := make(chan []byte, 8)
			f := oneShotFixture(t, nil, func(peer net.Conn) { io.Copy(peer, io.TeeReader(peer, peerReads{observed})) })
			cfg, signer := config(t, "restricted")
			var fault atomic.Bool
			fault.Store(true)
			rawTarget := make(chan *net.TCPConn, 1)
			s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
				c, raw, e := fixtureTarget(ctx, f.l.Addr().String())
				if e != nil {
					return nil, nil, e
				}
				rawTarget <- raw
				return &heldStop{c, &fault}, heldCutoff(raw, &fault), nil
			})
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
			serveDone := make(chan struct{})
			recordReady := make(chan net.Conn, 1)
			go func() {
				defer close(serveDone)
				c, e := l.Accept()
				if e != nil {
					return
				}
				raw := c.(*net.TCPConn)
				record := track(&tcpStream{raw})
				recordReady <- record
				if errors.Is(s.serve(&heldStop{record, &fault}, heldCutoff(raw, &fault)), ErrNotAccepted) {
					record.Close()
				}
			}()
			t.Cleanup(func() {
				fault.Store(false)
				l.Close()
				closeSession(t, s)
				select {
				case <-serveDone:
				case <-time.After(time.Second):
					t.Error("所有Serve未終了")
				}
				select {
				case c := <-recordReady:
					c.Close()
				default:
				}
			})
			cl, err := client(l.Addr().String(), cfg, signer, "key")
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
			assertPeerBytes(t, observed, []byte("harmless-regression-nonce"))
			raw := <-rawTarget
			peer := f.peer.Load()
			if peer == nil {
				t.Fatal("peerなし")
			}
			if err = s.Revoke(cfg.Identity); err != nil {
				t.Fatal(err)
			}
			awaitSnapshot(t, s, func(v Snapshot) bool { return v.State == "revoking" && v.CloseFailures == 2 && v.CutoffFailures == 2 })
			// nonceの生成と送信は論理失効が確定した後。失効前のqueueとは区別する。
			nonce := []byte("fresh-" + direction)
			if direction == "client-to-target" {
				ch.Write(nonce)
				assertChannelEndedWithoutData(t, ch)
				select {
				case data := <-observed:
					t.Fatalf("失効後の新規データが実targetへ到達: %q", data)
				case <-time.After(150 * time.Millisecond):
				}
			} else {
				if _, err = peer.Write(nonce); err != nil {
					t.Fatal(err)
				}
				assertChannelEndedWithoutData(t, ch)
			}
			// データ転送の拒否と物理切断を混同しない。独立raw経路のcanaryは依然到達する。
			canary := []byte("physical-still-live")
			if _, err = raw.Write(canary); err != nil {
				t.Fatal(err)
			}
			assertPeerBytes(t, observed, canary)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			err = s.Close(ctx)
			cancel()
			if err == nil || s.Snapshot().Owned != 2 {
				t.Fatal("物理故障を終了扱い")
			}
			t.Log("失効後の新規転送拒否、物理TCPはcanary到達のためNOT_MET、OS故障注入なし")
		})
	}
}
