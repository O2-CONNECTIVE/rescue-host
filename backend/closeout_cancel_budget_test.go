package backend

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestCloseoutN5CooperativeCancel(t *testing.T) {
	f := oneShotFixture(t, nil, func(net.Conn) { t.Error("取消済dialから接続が成立") })
	cfg, signer := config(t, "restricted")
	entered := make(chan struct{})
	cancelled := make(chan error, 1)
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		close(entered)
		<-ctx.Done()
		_, err := testDial(ctx, f.l.Addr().String())
		cancelled <- err
		return nil, nil, err
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
	opened := make(chan error, 1)
	go func() {
		ch, err := open(cl, "127.0.0.1", 22)
		if ch != nil {
			ch.Close()
		}
		opened <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dial未開始")
	}
	if s.Snapshot().Pending != 1 {
		t.Fatal("cancel前の枠が未登録")
	}
	trigger := time.Now()
	s.Revoke(cfg.Identity)
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("ctx取消が伝播しない")
		}
	case <-time.After(time.Second):
		t.Fatal("取消1秒超過")
	}
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("取消後channel公開")
		}
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("channel取消1秒超過")
	}
	ctx, cancel := context.WithDeadline(context.Background(), trigger.Add(time.Second))
	err = s.Close(ctx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.Pending != 0 || snap.Active != 0 || snap.Owned != 0 || snap.Transports != 0 {
		t.Fatal("取消後残留")
	}
	if f.peer.Load() != nil {
		t.Fatal("取消後に実fixture接続")
	}
}

type failedAccept struct {
	calls   atomic.Int64
	rejects atomic.Int64
}

func (c *failedAccept) ChannelType() string { return "direct-tcpip" }
func (c *failedAccept) ExtraData() []byte   { return ssh.Marshal(wireTarget{"127.0.0.1", 22, "x", 1}) }
func (c *failedAccept) Accept() (ssh.Channel, <-chan *ssh.Request, error) {
	c.calls.Add(1)
	return nil, nil, errors.New("試験Accept失敗")
}
func (c *failedAccept) Reject(ssh.RejectionReason, string) error { c.rejects.Add(1); return nil }
func TestCloseoutN5AcceptFailureClosesRealTarget(t *testing.T) {
	ended := make(chan error, 1)
	f := oneShotFixture(t, nil, func(peer net.Conn) { _, err := io.Copy(io.Discard, peer); ended <- err })
	cfg, _ := config(t, "restricted")
	s := dialFixtureSession(t, cfg, f)
	if s.Ready(cfg.Identity) != nil || s.Activate(cfg.Identity) != nil {
		t.Fatal("準備失敗")
	}
	defer closeSession(t, s)
	reservation, err := s.authority.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	open := &failedAccept{}
	s.workers.Add(1)
	trigger := time.Now()
	s.forward(open, reservation, nil)
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Active == 0 && x.Pending == 0 && x.Owned == 0 })
	select {
	case err := <-ended:
		if e, ok := err.(net.Error); ok && e.Timeout() {
			t.Fatal("timeoutをEOFと誤認")
		}
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("Accept失敗後target EOFなし")
	}
	if open.calls.Load() != 1 || open.rejects.Load() != 0 {
		t.Fatal("Accept失敗経路に到達しない")
	}
	t.Log("Accept errorは注入NewChannel、target終端は実TCPで観測")
}

type deadlineFailure struct{ net.Conn }

func (c *deadlineFailure) SetDeadline(time.Time) error { return errors.New("試験SetDeadline失敗") }
func TestCloseoutN5HandshakeSetupFailure(t *testing.T) {
	cfg, signer := config(t, "restricted")
	var dials atomic.Int64
	s := noTargetSession(t, cfg, &dials)
	if s.Ready(cfg.Identity) != nil || s.Activate(cfg.Identity) != nil {
		t.Fatal("準備失敗")
	}
	defer closeSession(t, s)
	l := testListen(t)
	done := make(chan error, 1)
	go func() {
		raw, err := l.Accept()
		if err != nil {
			done <- err
			return
		}
		conn := &deadlineFailure{Conn: track(raw)}
		err = s.Serve(conn)
		if errors.Is(err, ErrNotAccepted) {
			conn.Close()
		}
		done <- err
	}()
	cl, err := client(l.Addr().String(), cfg, signer, "key")
	if cl != nil {
		cl.Close()
	}
	if err == nil {
		t.Fatal("setup失敗で認証成功")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("setup失敗を成功にした")
		}
	case <-time.After(time.Second):
		t.Fatal("setup失敗終了超過")
	}
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Handshakes == 0 && x.Transports == 0 && x.Owned == 0 })
	if dials.Load() != 0 {
		t.Fatal("setup失敗でtarget接続")
	}
}

func TestCloseoutN6FourChannelsAcrossTransports(t *testing.T) {
	f := fixture(t)
	cfg, signer := config(t, "restricted")
	cfg.MaxChannels = 4
	cfg.MaxTransports = 2
	var dials atomic.Int64
	s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
		dials.Add(1)
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
	var clients []*ssh.Client
	var channels []ssh.Channel
	for n := 0; n < 2; n++ {
		cl, err := client(address, cfg, signer, "key")
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, cl)
		defer cl.Close()
	}
	for n := 0; n < 4; n++ {
		ch, err := open(clients[n%2], "127.0.0.1", 22)
		if err != nil {
			t.Fatal(err)
		}
		channels = append(channels, ch)
		defer ch.Close()
		echo(t, ch)
	}
	extra, err := open(clients[0], "127.0.0.1", 22)
	if extra != nil {
		extra.Close()
	}
	if err == nil || dials.Load() != 4 || s.Snapshot().Active != 4 {
		t.Fatal("5件目を拒否できない")
	}
	channels[0].Close()
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Active == 3 })
	reused, err := open(clients[1], "127.0.0.1", 22)
	if err != nil {
		t.Fatal("4枠の再利用失敗")
	}
	defer reused.Close()
	echo(t, reused)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		count := len(f.conns)
		f.mu.Unlock()
		if count == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	expected := map[net.Conn]bool{}
	f.mu.Lock()
	for c := range f.conns {
		expected[c] = true
	}
	f.mu.Unlock()
	if len(expected) != 4 {
		t.Fatal("4実targetの観測不一致")
	}
	trigger := time.Now()
	s.Revoke(cfg.Identity)
	for len(expected) > 0 {
		select {
		case event := <-f.events:
			if e, ok := event.err.(net.Error); ok && e.Timeout() {
				t.Fatal("timeoutをEOFと誤認")
			}
			delete(expected, event.conn)
		case <-time.After(time.Until(trigger.Add(time.Second))):
			t.Fatal("4実target終端1秒超過")
		}
	}
	ctx, cancel := context.WithDeadline(context.Background(), trigger.Add(time.Second))
	err = s.Close(ctx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.Active != 0 || snap.Pending != 0 || snap.Owned != 0 || snap.Transports != 0 {
		t.Fatal("4channel回収不完全")
	}
	t.Log("2transport/4channel/fifth rejected/reused/four peer EOF within1s")
}
func TestCloseoutN6EightHandshakeBoundary(t *testing.T) {
	cfg, signer := config(t, "full")
	cfg.MaxHandshakes = 8
	cfg.MaxTransports = 9
	var dials atomic.Int64
	s := noTargetSession(t, cfg, &dials)
	address := start(t, s, cfg)
	var held []net.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for n := 0; n < 8; n++ {
		conn, err := testDial(context.Background(), address)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Handshakes == 8 && x.Transports == 8 })
	ninth, err := client(address, cfg, signer, "key")
	if ninth != nil {
		ninth.Close()
	}
	if err == nil {
		t.Fatal("9件目handshakeを許可")
	}
	for _, c := range held {
		c.Close()
	}
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Handshakes == 0 && x.Transports == 0 })
	cl, err := client(address, cfg, signer, "key")
	if err != nil {
		t.Fatal("handshake枠再利用失敗")
	}
	cl.Close()
	if dials.Load() != 0 {
		t.Fatal("handshake試験でtarget接続")
	}
	t.Log("8held handshake/transport budget9/ninth rejected/slots reused")
}
