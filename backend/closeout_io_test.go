package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type oneFixture struct {
	l     *trackedListener
	peer  atomic.Pointer[trackedConn]
	ready chan struct{}
	done  chan struct{}
}

func oneShotFixture(t *testing.T, control func(string, string, syscall.RawConn) error, handler func(net.Conn)) *oneFixture {
	f := &oneFixture{l: testListenWithControl(t, control), ready: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(f.done)
		raw, err := f.l.Accept()
		if err != nil {
			return
		}
		c := track(raw).(*trackedConn)
		if control != nil {
			if err = raw.(*net.TCPConn).SetReadBuffer(fixtureReceiveSize()); err != nil {
				c.Close()
				t.Error(err)
				return
			}
		}
		f.peer.Store(c)
		close(f.ready)
		defer c.Close()
		c.SetDeadline(time.Now().Add(4 * time.Second))
		handler(c)
	}()
	t.Cleanup(func() {
		f.l.Close()
		if c := f.peer.Load(); c != nil {
			c.Close()
		}
		select {
		case <-f.done:
		case <-time.After(time.Second):
			t.Error("自己fixture終了未確認")
		}
	})
	return f
}
func dialFixtureSession(t *testing.T, cfg Config, f *oneFixture) *Session {
	t.Helper()
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
	return s
}
func channelResult(t *testing.T, ch ssh.Channel, expected []byte) {
	t.Helper()
	type outcome struct {
		data []byte
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(ch, int64(len(expected)+1)))
		done <- outcome{data, err}
	}()
	select {
	case result := <-done:
		if result.err != nil || !bytes.Equal(result.data, expected) {
			t.Fatalf("EOF後のresponse欠落: bytes=%d expected=%d err=%v", len(result.data), len(expected), result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("response/EOFが1秒以内に完了しない")
	}
}

func TestCloseoutN4HalfCloseReply(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), 4096)
			response := []byte("response-after-eof")
			f := oneShotFixture(t, nil, func(peer net.Conn) {
				received := make([]byte, len(payload))
				if _, err := io.ReadFull(peer, received); err != nil || !bytes.Equal(received, payload) {
					t.Error("fixture受信不一致")
					return
				}
				var extra [1]byte
				if n, err := peer.Read(extra[:]); n != 0 || err != io.EOF {
					t.Error("fixtureでEOF未観測")
					return
				}
				peer.Write(response)
			})
			cfg, signer := config(t, mode)
			s := dialFixtureSession(t, cfg, f)
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
			if _, err = ch.Write(payload); err != nil {
				t.Fatal(err)
			}
			if err = ch.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			channelResult(t, ch, response)
			awaitSnapshot(t, s, func(x Snapshot) bool { return x.Active == 0 && x.Pending == 0 })
			t.Log("client_eof_received=true response_after_eof_preserved=true request_bytes=4096")
		})
	}
}

func TestCloseoutN4ServerEOFKeepsClientDirection(t *testing.T) {
	response := []byte("server-half-close")
	payload := bytes.Repeat([]byte("y"), 4096)
	received := make(chan []byte, 1)
	f := oneShotFixture(t, nil, func(peer net.Conn) {
		peer.Write(response)
		if err := peer.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
			t.Error(err)
			return
		}
		data, err := io.ReadAll(io.LimitReader(peer, 4097))
		if err != nil {
			t.Error(err)
		}
		received <- data
	})
	cfg, signer := config(t, "restricted")
	s := dialFixtureSession(t, cfg, f)
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
	channelResult(t, ch, response)
	if _, err = ch.Write(payload); err != nil {
		t.Fatal("peer EOF後の逆方向Writeが失われた")
	}
	if err = ch.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-received:
		if !bytes.Equal(data, payload) {
			t.Fatal("逆方向request欠落")
		}
	case <-time.After(time.Second):
		t.Fatal("逆方向完了が1秒超過")
	}
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Active == 0 && x.Pending == 0 })
}

type tcpWriteOutcome struct {
	n   int
	err error
}

// 各呼出しの完了値を別に保持し、後続Writeの値で観測を置き換えない。
type tcpWriteCall struct {
	bytes    int
	sequence int64
	result   atomic.Pointer[tcpWriteOutcome]
}
type pendingTCPWrite struct {
	call atomic.Pointer[tcpWriteCall]
	last atomic.Pointer[tcpWriteOutcome]
	net.Conn
	started, finished atomic.Int64
	written           atomic.Int64
}

func (c *pendingTCPWrite) Write(p []byte) (int, error) {
	call := &tcpWriteCall{bytes: len(p), sequence: c.started.Add(1)}
	c.call.Store(call)
	n, err := c.Conn.Write(p)
	outcome := &tcpWriteOutcome{n, err}
	call.result.Store(outcome)
	c.last.Store(outcome)
	c.written.Add(int64(n))
	c.finished.Add(1)
	return n, err
}
func (c *pendingTCPWrite) CloseWrite() error {
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}

func TestCloseoutN4IdleAndBackpressureRevocation(t *testing.T) {
	for _, kind := range []string{"no-read", "no-write", "tcp-backpressure"} {
		t.Run(kind, func(t *testing.T) {
			release := make(chan struct{})
			var control func(string, string, syscall.RawConn) error
			var targetControl func(string, string, syscall.RawConn) error
			if kind == "tcp-backpressure" {
				control = smallFixtureReceiveBuffer
				targetControl = fixtureBackpressureControl()
			}
			peerRead := make(chan error, 1)
			peerProgress := make(chan error, 1)
			f := oneShotFixture(t, control, func(peer net.Conn) {
				if kind == "tcp-backpressure" {
					var first [1]byte
					_, err := io.ReadFull(peer, first[:])
					if err == nil && first[0] != 'p' {
						err = errors.New("自己fixtureの先頭byteが不一致")
					}
					peerProgress <- err
					if err != nil {
						return
					}
				}
				if kind != "no-write" {
					<-release
				}
				_, err := io.Copy(io.Discard, peer)
				peerRead <- err
			})
			cfg, signer := config(t, "restricted")
			var writer atomic.Pointer[pendingTCPWrite]
			var targetRaw atomic.Pointer[net.TCPConn]
			s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
				conn, raw, err := fixtureTargetWithControl(ctx, f.l.Addr().String(), targetControl)
				if err != nil {
					t.Logf("自己fixtureのTCP接続準備に失敗: %v", err)
					return nil, nil, err
				}
				if kind == "tcp-backpressure" {
					// accept側の受信buffer設定完了後に初回Writeを開始する。
					select {
					case <-f.ready:
					case <-f.done:
						conn.Close()
						return nil, nil, errors.New("自己fixtureの受信準備が失敗")
					case <-time.After(200 * time.Millisecond):
						conn.Close()
						return nil, nil, errors.New("自己fixtureの受信準備が到達予算を超過")
					}
					if err = raw.SetWriteBuffer(fixtureSendSize()); err != nil {
						conn.Close()
						return nil, nil, err
					}
				}
				targetRaw.Store(raw)
				v := &pendingTCPWrite{Conn: conn}
				writer.Store(v)
				return v, tcpCutoff(raw), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			address := start(t, s, cfg)
			t.Cleanup(func() { onceRelease(release) })
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
			if kind == "tcp-backpressure" {
				peer := f.peer.Load().Conn.(*net.TCPConn)
				profile, profileErr := checkFixtureBackpressureProfile(targetRaw.Load(), peer)
				if profileErr != nil {
					t.Fatal(profileErr)
				}
				peerSend, peerReceive, socketErr := ownedSocketBuffers(peer)
				if socketErr != nil {
					t.Fatal(socketErr)
				}
				t.Logf("before_first_write %s peer_send_buffer=%d peer_receive_buffer=%d", profile, peerSend, peerReceive)
			}
			amount := 4096
			if kind == "tcp-backpressure" {
				amount = 65536
			}
			if n, writeErr := ch.Write(bytes.Repeat([]byte("p"), amount)); writeErr != nil || n != amount {
				err = writeErr
				t.Fatal(err)
			}
			if kind != "tcp-backpressure" {
				deadline := time.Now().Add(200 * time.Millisecond)
				for writer.Load().finished.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if writer.Load().finished.Load() == 0 {
					t.Fatal("idleケースの実Write未到達")
				}
			}
			var blocked *tcpWriteCall
			if kind == "tcp-backpressure" {
				select {
				case err := <-peerProgress:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(200 * time.Millisecond):
					t.Fatal("同一TCPの先頭byteを到達予算内に受信できない。背圧は未達")
				}
				sendSize, _, socketErr := ownedSocketBuffers(targetRaw.Load())
				if socketErr != nil {
					t.Fatal(socketErr)
				}
				recvSize := -1
				if peer := f.peer.Load(); peer != nil {
					_, recvSize, socketErr = ownedSocketBuffers(peer.Conn.(*net.TCPConn))
					if socketErr != nil {
						t.Fatal(socketErr)
					}
				}
				t.Logf("owned_socket_send_buffer=%d peer_receive_buffer=%d fixed_data_bytes=%d", sendSize, recvSize, amount)
				observeUntil := time.Now().Add(700 * time.Millisecond)
				for time.Now().Before(observeUntil) {
					call := writer.Load().call.Load()
					if call != nil && call.sequence == 1 && call.result.Load() == nil {
						time.Sleep(50 * time.Millisecond)
						if call.result.Load() == nil {
							time.Sleep(50 * time.Millisecond)
							if call.result.Load() == nil {
								blocked = call
								break
							}
						}
					}
					time.Sleep(5 * time.Millisecond)
				}
				if blocked == nil {
					t.Fatalf("固定量・有限観測内で同一実TCP Write継続未完了を観測できない。未達を維持: started=%d finished=%d written=%d last=%+v", writer.Load().started.Load(), writer.Load().finished.Load(), writer.Load().written.Load(), writer.Load().last.Load())
				}
			}
			// 安定観測と失効の間へ診断logやsocket読取りを挟まない。
			trigger := time.Now()
			if err = s.Revoke(cfg.Identity); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithDeadline(context.Background(), trigger.Add(time.Second))
			err = s.Close(ctx)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if blocked != nil {
				result := blocked.result.Load()
				if result != nil {
					t.Logf("peer_first_byte_received_before_revoke=true same_write_sequence=1 write_return_bytes=%d write_error=%v local_close=%t", result.n, result.err, errors.Is(result.err, net.ErrClosed))
				}
				if result == nil || result.n >= blocked.bytes || !errors.Is(result.err, net.ErrClosed) {
					if result == nil {
						t.Fatal("観測した同一実TCP Writeが失効後も未完了。背圧は未達")
					}
					t.Fatalf("観測した同一実TCP Writeの失効による中断を確認できない。背圧は未達: submitted=%d n=%d error=%v local_close=%t", blocked.bytes, result.n, result.err, errors.Is(result.err, net.ErrClosed))
				}
				t.Logf("peer_first_byte_received=true same_write_sequence=1 stable_pending_write_samples=2 interval_ms=50 same_write_submitted=%d write_return_bytes=%d local_close=true", blocked.bytes, result.n)
			}
			onceRelease(release)
			select {
			case err := <-peerRead:
				if e, ok := err.(net.Error); ok && e.Timeout() {
					t.Fatal("peer timeoutをEOFと誤認")
				}
			case <-time.After(time.Until(trigger.Add(time.Second))):
				t.Fatal("peer終端1秒超過")
			}
			if snap := s.Snapshot(); snap.Owned != 0 || snap.Active != 0 || snap.Pending != 0 || snap.Transports != 0 {
				t.Fatal("失効後残留")
			}
			sshEnded := make(chan error, 1)
			go func() { sshEnded <- cl.Wait() }()
			select {
			case <-sshEnded:
			case <-time.After(time.Until(trigger.Add(time.Second))):
				t.Fatal("SSH側終端1秒超過")
			}
			if n, err := ch.Write([]byte("fresh-after-revoke")); n != 0 || err == nil {
				t.Fatal("失効後の新規送信")
			}
			t.Logf("kind=%s bounded_bytes=%d tcp_write_started=%d finished=%d end_ms=%d owned=0", kind, amount, writer.Load().started.Load(), writer.Load().finished.Load(), time.Since(trigger).Milliseconds())
		})
	}
}
