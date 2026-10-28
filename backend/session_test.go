package backend

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// 鍵、fixture、宛先差替え、ソケット記録は全てテストビルドだけに置く。
var network = struct {
	sync.Mutex
	listeners      map[string]bool
	conns          map[*trackedConn]bool
	history, dials []string
	forbidden      int
}{listeners: map[string]bool{}, conns: map[*trackedConn]bool{}}

type trackedConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		if c.err == nil {
			network.Lock()
			delete(network.conns, c)
			network.Unlock()
		}
	})
	return c.err
}

// 試験の追跡wrapperでも実TCPの半閉鎖能力を隠さない。
func (c *trackedConn) CloseWrite() error {
	if w, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return errors.New("このfixture接続は半閉鎖に未対応")
}
func track(c net.Conn) net.Conn {
	v := &trackedConn{Conn: c}
	network.Lock()
	network.conns[v] = true
	network.Unlock()
	return v
}

type trackedListener struct {
	net.Listener
	once sync.Once
}

func (l *trackedListener) Close() error {
	l.once.Do(func() {
		network.Lock()
		delete(network.listeners, l.Addr().String())
		l.Listener.Close()
		network.Unlock()
	})
	return nil
}
func testListen(t *testing.T) *trackedListener { return testListenWithControl(t, nil) }
func testListenWithControl(t *testing.T, control func(string, string, syscall.RawConn) error) *trackedListener {
	t.Helper()
	lc := net.ListenConfig{Control: control}
	l, e := lc.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	a := l.Addr().(*net.TCPAddr)
	if !a.IP.Equal(net.IPv4(127, 0, 0, 1)) || a.Port <= 1024 || a.Port == 22 {
		l.Close()
		t.Fatal("試験以外の待受を禁止")
	}
	network.Lock()
	network.listeners[l.Addr().String()] = true
	network.history = append(network.history, l.Addr().String())
	network.Unlock()
	v := &trackedListener{Listener: l}
	t.Cleanup(func() { v.Close() })
	return v
}
func testDial(ctx context.Context, address string) (net.Conn, error) {
	return testDialWithControl(ctx, address, nil)
}
func testDialWithControl(ctx context.Context, address string, control func(string, string, syscall.RawConn) error) (net.Conn, error) {
	// 接続完了まで所有 listener の close と同じ lock を使い、ポート再利用を避ける。
	network.Lock()
	defer network.Unlock()
	host, port, e := net.SplitHostPort(address)
	p, pe := strconv.Atoi(port)
	if e != nil || pe != nil || host != "127.0.0.1" || p <= 1024 || p == 22 || !network.listeners[address] {
		network.forbidden++
		return nil, errors.New("所有外への試験接続を禁止")
	}
	c, e := (&net.Dialer{Timeout: time.Second, Control: control}).DialContext(ctx, "tcp4", address)
	if e != nil {
		return nil, e
	}
	v := &trackedConn{Conn: c}
	network.conns[v] = true
	network.dials = append(network.dials, address)
	return v, nil
}

type echoFixture struct {
	l       *trackedListener
	mu      sync.Mutex
	closing bool
	conns   map[net.Conn]bool
	wg      sync.WaitGroup
	events  chan fixtureEvent
}
type fixtureEvent struct {
	conn net.Conn
	err  error
}

func fixture(t *testing.T) *echoFixture {
	f := &echoFixture{l: testListen(t), conns: map[net.Conn]bool{}, events: make(chan fixtureEvent, 64)}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			raw, e := f.l.Accept()
			if e != nil {
				return
			}
			c := track(raw)
			f.mu.Lock()
			if f.closing {
				f.mu.Unlock()
				c.Close()
				continue
			}
			f.conns[c] = true
			f.wg.Add(1)
			f.mu.Unlock()
			go func() {
				defer f.wg.Done()
				defer func() { c.Close(); f.mu.Lock(); delete(f.conns, c); f.mu.Unlock() }()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				_, err := io.Copy(c, c)
				f.events <- fixtureEvent{c, err}
			}()
		}
	}()
	t.Cleanup(func() {
		f.mu.Lock()
		f.closing = true
		f.mu.Unlock()
		f.l.Close()
		f.mu.Lock()
		for c := range f.conns {
			c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}
func key(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	k, e := ssh.NewSignerFromKey(private)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func config(t *testing.T, mode string) (Config, ssh.Signer) {
	t.Helper()
	a := key(t)
	digest, e := PolicyDigest(mode)
	if e != nil {
		t.Fatal(e)
	}
	return Config{Identity: Identity{LaunchID: "test-launch", Epoch: "test-epoch", PolicyDigest: digest}, OperationMode: mode, AllowedKeys: []ssh.PublicKey{a.PublicKey()}, HostKey: key(t), MaximumDuration: 5 * time.Second, LeaseDuration: 5 * time.Second, MaxChannels: 2, MaxHandshakes: 2}, a
}
func closeSession(t *testing.T, s *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := s.Close(ctx); e != nil {
		t.Error(e)
	}
}
func start(t *testing.T, s *Session, c Config) string {
	t.Helper()
	if e := s.Ready(c.Identity); e != nil {
		t.Fatal(e)
	}
	if e := s.Activate(c.Identity); e != nil {
		t.Fatal(e)
	}
	l := testListen(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			raw, e := l.Accept()
			if e != nil {
				return
			}
			conn := track(raw)
			wg.Add(1)
			go func() {
				defer wg.Done()
				e := s.Serve(conn)
				if errors.Is(e, ErrNotAccepted) {
					conn.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() { l.Close(); closeSession(t, s); wg.Wait() })
	return l.Addr().String()
}
func client(address string, c Config, signer ssh.Signer, authMethod string) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, e := testDial(ctx, address)
	if e != nil {
		return nil, e
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	cfg := &ssh.ClientConfig{User: c.Identity.LaunchID, HostKeyCallback: ssh.FixedHostKey(c.HostKey.PublicKey())}
	switch authMethod {
	case "key":
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	case "password":
		cfg.Auth = []ssh.AuthMethod{ssh.Password("invalid-disposable")}
	case "keyboard":
		cfg.Auth = []ssh.AuthMethod{ssh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil })}
	}
	sc, chans, reqs, e := ssh.NewClientConn(conn, address, cfg)
	if e != nil {
		conn.Close()
		return nil, e
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), nil
}

type wireTarget struct {
	Host       string
	Port       uint32
	Origin     string
	OriginPort uint32
}

func open(c *ssh.Client, host string, port uint32) (ssh.Channel, error) {
	ch, reqs, e := c.OpenChannel("direct-tcpip", ssh.Marshal(wireTarget{host, port, "203.0.113.9", 1234}))
	if e == nil {
		go ssh.DiscardRequests(reqs)
	}
	return ch, e
}
func echo(t *testing.T, ch ssh.Channel) {
	t.Helper()
	data := []byte("harmless-regression-nonce")
	if _, e := ch.Write(data); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, len(data))
	if _, e := io.ReadFull(ch, b); e != nil || !bytes.Equal(data, b) {
		t.Fatalf("fixture 往復失敗: %v", e)
	}
}

func TestPublicConfigurationAndLifecycle(t *testing.T) {
	c, _ := config(t, "restricted")
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	defer closeSession(t, s)
	if s.Snapshot().State != "prepared" {
		t.Fatal("準備状態ではない")
	}
	bad := c.Identity
	bad.Epoch = "other"
	if s.Ready(bad) == nil || s.Activate(c.Identity) == nil {
		t.Fatal("不正遷移を許可")
	}
	c.Identity.PolicyDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, e = New(c); e == nil {
		t.Fatal("方針不一致を許可")
	}
}

// 誤った鍵で生成した既存ライブラリの署名を、許可鍵の署名として申告する負例。
type wrongSignature struct {
	ssh.Signer
	claimed ssh.PublicKey
}

func (w wrongSignature) PublicKey() ssh.PublicKey { return w.claimed }

func TestAuthentication(t *testing.T) {
	for _, which := range []string{"valid", "wrong-key", "wrong-user", "none", "password", "keyboard", "wrong-signature", "wrong-host"} {
		t.Run(which, func(t *testing.T) {
			c, a := config(t, "restricted")
			s, e := newSession(c, func(context.Context) (net.Conn, error) {
				t.Error("認証試験で dial")
				return nil, errors.New("禁止")
			})
			if e != nil {
				t.Fatal(e)
			}
			address := start(t, s, c)
			signer := a
			cc := c
			method := "key"
			switch which {
			case "wrong-key":
				signer = key(t)
			case "wrong-user":
				cc.Identity.LaunchID = "other"
			case "none":
				method = "none"
			case "password":
				method = "password"
			case "keyboard":
				method = "keyboard"
			case "wrong-signature":
				signer = wrongSignature{key(t), a.PublicKey()}
			case "wrong-host":
				cc.HostKey = key(t)
			}
			cl, e := client(address, cc, signer, method)
			if cl != nil {
				defer cl.Close()
			}
			if (e == nil) != (which == "valid") {
				t.Fatalf("認証結果不一致: %v", e)
			}
		})
	}
}

func TestPolicyAndRequestRejections(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			c, a := config(t, mode)
			var dials atomic.Int64
			s, e := newSession(c, func(context.Context) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("試験で本番 dial を禁止")
			})
			if e != nil {
				t.Fatal(e)
			}
			address := start(t, s, c)
			cl, e := client(address, c, a, "key")
			if e != nil {
				t.Fatal(e)
			}
			defer cl.Close()
			for _, kind := range []string{"session", "forwarded-tcpip", "x11", "auth-agent@openssh.com", "direct-streamlocal@openssh.com", "unknown"} {
				ch, _, e := cl.OpenChannel(kind, nil)
				if ch != nil {
					ch.Close()
				}
				if e == nil {
					t.Fatal("禁止チャネルを許可")
				}
			}
			for _, kind := range []string{"tcpip-forward", "cancel-tcpip-forward", "unknown"} {
				for _, reply := range []bool{false, true} {
					ok, _, e := cl.SendRequest(kind, reply, ssh.Marshal(struct {
						Host string
						Port uint32
					}{"127.0.0.1", 40000}))
					if e != nil || ok {
						t.Fatal("global request を許可")
					}
				}
			}
			for _, host := range []string{"localhost", "127.1", "127.000.000.001", "2130706433", "0x7f000001", "", " 127.0.0.1", "127.0.0.1 ", "127.0.0.1\x00", "::1", "::ffff:127.0.0.1", "0.0.0.0", "127.0.0.2", "192.0.2.1", "203.0.113.1", "example.invalid"} {
				ch, e := open(cl, host, 22)
				if ch != nil {
					ch.Close()
				}
				if e == nil {
					t.Fatal("禁止先を許可")
				}
			}
			for _, port := range []uint32{0, 21, 23, 65535, 0xffffffff} {
				ch, e := open(cl, "127.0.0.1", port)
				if ch != nil {
					ch.Close()
				}
				if e == nil {
					t.Fatal("禁止ポートを許可")
				}
			}
			for _, data := range [][]byte{nil, {0, 0, 0, 255}, append(ssh.Marshal(wireTarget{"127.0.0.1", 22, "x", 1}), 1), make([]byte, 1025)} {
				ch, _, e := cl.OpenChannel("direct-tcpip", data)
				if ch != nil {
					ch.Close()
				}
				if e == nil {
					t.Fatal("不正 payload を許可")
				}
			}
			if dials.Load() != 0 {
				t.Fatal("拒否時に dial")
			}
		})
	}
}

func fixtureRequestErrorClass(err error) string {
	switch {
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed), errors.Is(err, io.ErrClosedPipe):
		return "closed"
	default:
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "timeout"
		}
		return "other"
	}
}

func TestFixtureForwardAndExpiry(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		for _, cause := range []string{"explicit", "lease", "maximum"} {
			t.Run(mode+"/"+cause, func(t *testing.T) {
				f := fixture(t)
				c, a := config(t, mode)
				if cause == "lease" {
					c.LeaseDuration = 250 * time.Millisecond
				}
				if cause == "maximum" {
					c.MaximumDuration = 450 * time.Millisecond
					c.LeaseDuration = 200 * time.Millisecond
				}
				fixtureStart := time.Now()
				s, e := newSession(c, func(ctx context.Context) (net.Conn, error) { return testDial(ctx, f.l.Addr().String()) })
				if e != nil {
					t.Fatal(e)
				}
				address := start(t, s, c)
				var renewals atomic.Int64
				if cause == "maximum" {
					stop, joined := make(chan struct{}), make(chan struct{})
					t.Cleanup(func() {
						close(stop)
						select {
						case <-joined:
						case <-time.After(time.Second):
							t.Error("maximum fixtureの更新worker終了未確認")
						}
					})
					// 認証や要求確認の間も短leaseを維持し、最大期限だけを観測する。
					go func() {
						defer close(joined)
						ticker := time.NewTicker(50 * time.Millisecond)
						defer ticker.Stop()
						for n := uint64(1); ; n++ {
							select {
							case <-stop:
								return
							case <-ticker.C:
							}
							if s.Renew(c.Identity, n) != nil {
								return
							}
							renewals.Add(1)
						}
					}()
				}
				cl, e := client(address, c, a, "key")
				if e != nil {
					t.Fatal(e)
				}
				defer cl.Close()
				ch, e := open(cl, "127.0.0.1", 22)
				if e != nil {
					t.Fatal(e)
				}
				defer ch.Close()
				echo(t, ch)
				for _, kind := range []string{"shell", "exec", "pty-req", "subsystem", "env", "x11-req", "auth-agent-req@openssh.com"} {
					var payload []byte
					switch kind {
					case "exec":
						payload = ssh.Marshal(struct{ Command string }{"must-not-execute"})
					case "pty-req":
						payload = ssh.Marshal(struct {
							Term                         string
							Columns, Rows, Width, Height uint32
							Modes                        string
						}{"dumb", 80, 24, 0, 0, "\x00"})
					case "subsystem":
						payload = ssh.Marshal(struct{ Name string }{"sftp"})
					case "env":
						payload = ssh.Marshal(struct{ Name, Value string }{"PROBE_ONLY", "unused"})
					case "x11-req":
						payload = ssh.Marshal(struct {
							Single           bool
							Protocol, Cookie string
							Screen           uint32
						}{false, "MIT-MAGIC-COOKIE-1", "00", 0})
					}
					ok, e := ch.SendRequest(kind, true, payload)
					if e != nil {
						snap := s.Snapshot()
						t.Fatalf("TCP要求の拒否応答を確認できない: phase=policy kind=%s elapsed_ns=%d error_class=%s allowed=%t renewal_successes=%d state=%s reason=%s", kind, time.Since(fixtureStart).Nanoseconds(), fixtureRequestErrorClass(e), ok, renewals.Load(), snap.State, snap.Reason)
					}
					if ok {
						snap := s.Snapshot()
						t.Fatalf("TCP上の要求を許可: phase=policy kind=%s elapsed_ns=%d allowed=true renewal_successes=%d state=%s reason=%s", kind, time.Since(fixtureStart).Nanoseconds(), renewals.Load(), snap.State, snap.Reason)
					}
				}
				echo(t, ch)
				if cause == "explicit" {
					s.Revoke(c.Identity)
				}
				done := make(chan error, 1)
				go func() { var b [1]byte; _, e := ch.Read(b[:]); done <- e }()
				select {
				case e := <-done:
					if e == nil {
						t.Fatal("失効後もデータ受信")
					}
				case <-time.After(time.Second):
					t.Fatal("失効が遅い")
				}
				select {
				case event := <-f.events:
					if n, ok := event.err.(net.Error); ok && n.Timeout() {
						t.Fatal("fixture の期限切れを切断と誤認")
					}
				case <-time.After(time.Second):
					t.Fatal("転送先側の EOF がない")
				}
				snap := s.Snapshot()
				t.Logf("phase=expired cause=%s elapsed_ns=%d renewal_successes=%d state=%s reason=%s", cause, time.Since(fixtureStart).Nanoseconds(), renewals.Load(), snap.State, snap.Reason)
				if cause == "maximum" && (renewals.Load() == 0 || snap.Reason != "maximum-deadline") {
					t.Fatal("更新成功後の最大期限失効を確認できない")
				}
				if s.Renew(c.Identity, ^uint64(0)) == nil {
					t.Fatal("失効後に復活")
				}
				nc, e := client(address, c, a, "key")
				if nc != nil {
					nc.Close()
				}
				if e == nil {
					t.Fatal("失効後の再接続を許可")
				}
			})
		}
	}
}

func TestPendingDialRevocationAndCapacity(t *testing.T) {
	f := fixture(t)
	c, a := config(t, "restricted")
	c.MaxChannels = 1
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s, e := newSession(c, func(context.Context) (net.Conn, error) {
		once.Do(func() { close(entered) })
		<-release
		return testDial(context.Background(), f.l.Addr().String())
	})
	if e != nil {
		t.Fatal(e)
	}
	address := start(t, s, c)
	cl, e := client(address, c, a, "key")
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	defer onceRelease(release)
	done := make(chan error, 1)
	go func() {
		ch, e := open(cl, "127.0.0.1", 22)
		if ch != nil {
			ch.Close()
		}
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dial に入らない")
	}
	ch, e := open(cl, "127.0.0.1", 22)
	if ch != nil {
		ch.Close()
	}
	if e == nil {
		t.Fatal("接続中の枠を数えていない")
	}
	s.Revoke(c.Identity)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	e = s.Close(ctx)
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) || s.Snapshot().Pending != 1 {
		t.Fatal("遅延 dial の参照を失った")
	}
	onceRelease(release)
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("失効中の接続を公開")
		}
	case <-time.After(time.Second):
		t.Fatal("開設が終了しない")
	}
	closeSession(t, s)
	if s.Snapshot().State != "terminated" {
		t.Fatal("終了未確認")
	}
}
func onceRelease(c chan struct{}) {
	select {
	case <-c:
	default:
		close(c)
	}
}

type failedClose struct {
	net.Conn
	fail  atomic.Bool
	calls atomic.Int64
	block <-chan struct{}
}

func (c *failedClose) Close() error {
	c.calls.Add(1)
	if c.block != nil {
		<-c.block
	}
	if c.fail.Load() {
		return errors.New("試験の close 失敗")
	}
	return c.Conn.Close()
}

// 回帰成功と故障中の物理切断受入を分け、未達も証拠へ残す。
var faultEvidence map[string]any

func TestCloseFailureRetainsAndRevokes(t *testing.T) {
	f := fixture(t)
	c, a := config(t, "restricted")
	var faulty atomic.Pointer[failedClose]
	s, e := newSessionWithCutoff(c, func(ctx context.Context) (net.Conn, func() error, error) {
		conn, e := testDial(ctx, f.l.Addr().String())
		if e != nil {
			return nil, nil, e
		}
		rawTCP := conn.(*trackedConn).Conn.(*net.TCPConn)
		conn.(*trackedConn).Conn = &tcpStream{rawTCP}
		value := &failedClose{Conn: conn}
		value.fail.Store(true)
		faulty.Store(value)
		return value, func() error {
			if value.fail.Load() {
				return errors.New("独立停止経路の試験故障。実OSclose故障ではない")
			}
			return tcpCutoff(rawTCP)()
		}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	address := start(t, s, c)
	t.Cleanup(func() {
		if value := faulty.Load(); value != nil {
			value.fail.Store(false)
		}
	})
	cl, e := client(address, c, a, "key")
	if e != nil {
		t.Fatal(e)
	}
	defer cl.Close()
	control, e := client(address, c, a, "key")
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	ch, e := open(cl, "127.0.0.1", 22)
	if e != nil {
		t.Fatal(e)
	}
	echo(t, ch)
	f.mu.Lock()
	var peer net.Conn
	for conn := range f.conns {
		peer = conn
	}
	f.mu.Unlock()
	if peer == nil {
		t.Fatal("fixture 側の接続がない")
	}
	trigger := time.Now()
	ch.Close()
	deadline := time.Now().Add(time.Second)
	for (s.Snapshot().State != "revoking" || s.Snapshot().CloseFailures == 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snap := s.Snapshot(); snap.State != "revoking" || snap.Owned == 0 || snap.CloseFailures == 0 {
		t.Fatal("明示 Close 前に故障だけで失効していない")
	}
	revokeMS := time.Since(trigger).Milliseconds()
	revokingObserved := time.Now()
	sshClosed := make(chan error, 1)
	go func() { sshClosed <- control.Wait() }()
	select {
	case e := <-sshClosed:
		if e == nil {
			t.Fatal("SSH 側の切断を観測できない")
		}
	case <-time.After(time.Second):
		t.Fatal("SSH 側が切断されない")
	}
	next, e := client(address, c, a, "key")
	if next != nil {
		next.Close()
	}
	if e == nil {
		t.Fatal("故障後の新認証を許可")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	e = s.Close(ctx)
	cancel()
	if (!errors.Is(e, ErrUnconfirmed) && !errors.Is(e, context.DeadlineExceeded)) || s.Snapshot().Owned == 0 || s.Snapshot().State != "revoking" {
		t.Fatalf("故障を終了扱い: %v", e)
	}
	if s.Renew(c.Identity, ^uint64(0)) == nil {
		t.Fatal("故障中の更新を許可")
	}
	// 閉じた SSH へコピー中の reader を起こし、転送処理終了後の生 TCP を観測する。
	peer.SetWriteDeadline(time.Now().Add(time.Second))
	if _, e = peer.Write([]byte("wake-closed-channel")); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(time.Second)
	for s.Snapshot().Forwarding != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Snapshot().Forwarding != 0 {
		t.Fatal("コピー処理が終わらない")
	}
	if s.Snapshot().Active != 1 {
		t.Fatal("終了未確認targetの容量を保持していない")
	}
	raw := faulty.Load().Conn
	raw.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	var buffer [128]byte
	for {
		_, e = raw.Read(buffer[:])
		if e != nil {
			if n, ok := e.(net.Error); !ok || !n.Timeout() {
				t.Fatal("残存 TCP の状態が想定と異なる")
			}
			break
		}
	}
	if wait := time.Until(revokingObserved.Add(time.Second)); wait > 0 {
		time.Sleep(wait)
	}
	raw.SetReadDeadline(time.Now().Add(time.Second))
	peer.SetWriteDeadline(time.Now().Add(time.Second))
	canary := []byte("fresh-after-revoke")
	if _, e = peer.Write(canary); e != nil {
		t.Fatal(e)
	}
	received := make([]byte, len(canary))
	if _, e = io.ReadFull(raw, received); e != nil || !bytes.Equal(canary, received) {
		t.Fatal("残存 TCP の両端観測に失敗")
	}
	canaryMS := time.Since(revokingObserved).Milliseconds()
	faulty.Load().fail.Store(false)
	closeSession(t, s)
	select {
	case event := <-f.events:
		if event.conn != peer {
			t.Fatal("別接続の終了応答")
		}
		if n, ok := event.err.(net.Error); ok && n.Timeout() {
			t.Fatal("fixture の期限切れを EOF と誤認")
		}
	case <-time.After(time.Second):
		t.Fatal("復旧後の fixture EOF がない")
	}
	if s.Snapshot().Owned != 0 {
		t.Fatal("復旧後も所有対象が残る")
	}
	faultEvidence = map[string]any{"fault_kind": "adapter_error_and_independent_cutoff_error_injected", "real_os_close_failure_tested": false, "revoking_before_explicit_close": true, "automatic_revocation_ms": revokeMS, "ssh_transport_closed": true, "new_authentication_rejected": true, "renewal_rejected": true, "target_tcp_live_after_close_budget": true, "physical_canary_ms_after_revoke": canaryMS, "target_peer_eof_after_recovery": true, "forced_tcp_cutoff_under_close_failure": "NOT_MET: アダプターと独立停止経路の双方を試験故障にするとTCPが残る。実OSclose故障ではない"}
}

func TestBlockedCloseHasSingleAttempt(t *testing.T) {
	c, _ := config(t, "restricted")
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	block := make(chan struct{})
	t.Cleanup(func() { onceRelease(block); closeSession(t, s) })
	left, right := net.Pipe()
	defer right.Close()
	fault := &failedClose{Conn: left, block: block}
	s.mu.Lock()
	s.ownLocked(fault)
	s.mu.Unlock()
	for n := 0; n < 10; n++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		s.Close(ctx)
		cancel()
	}
	if fault.calls.Load() != 1 || s.Snapshot().Closing != 1 {
		t.Fatal("close 再試行が無制限に増えた")
	}
	close(block)
	closeSession(t, s)
}

func TestConcurrentCloseRetries(t *testing.T) {
	c, _ := config(t, "restricted")
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	left, right := net.Pipe()
	defer right.Close()
	fault := &failedClose{Conn: left}
	fault.fail.Store(true)
	s.mu.Lock()
	s.ownLocked(fault)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 10; k++ {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				s.Close(ctx)
				cancel()
			}
		}()
	}
	wg.Wait()
	fault.fail.Store(false)
	closeSession(t, s)
}

func TestHandshakeRevocation(t *testing.T) {
	c, _ := config(t, "restricted")
	s, e := newSession(c, func(context.Context) (net.Conn, error) {
		t.Error("handshake 中に dial")
		return nil, errors.New("禁止")
	})
	if e != nil {
		t.Fatal(e)
	}
	address := start(t, s, c)
	conn, e := testDial(context.Background(), address)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().Handshakes == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.Revoke(c.Identity)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 1024)
	for {
		_, e = conn.Read(b)
		if e != nil {
			break
		}
	}
	if n, ok := e.(net.Error); ok && n.Timeout() {
		t.Fatal("認証前接続が閉じない")
	}
}

// 試験用の冪等ラッパーが製品側の二重 close を隠さないことも検査する。
type strictClose struct {
	net.Conn
	calls atomic.Int64
}

func (c *strictClose) Close() error {
	if c.calls.Add(1) != 1 {
		return net.ErrClosed
	}
	return c.Conn.Close()
}
func TestLibraryCloseIsOwnedExactlyOnce(t *testing.T) {
	c, _ := config(t, "restricted")
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	if s.Ready(c.Identity) != nil || s.Activate(c.Identity) != nil {
		t.Fatal("準備失敗")
	}
	l := testListen(t)
	done := make(chan *strictClose, 1)
	go func() {
		raw, e := l.Accept()
		if e != nil {
			done <- nil
			return
		}
		strict := &strictClose{Conn: track(raw)}
		e = s.Serve(strict)
		if errors.Is(e, ErrNotAccepted) {
			strict.Close()
		}
		done <- strict
	}()
	t.Cleanup(func() { l.Close(); closeSession(t, s) })
	cl, e := client(l.Addr().String(), c, key(t), "key")
	if cl != nil {
		cl.Close()
	}
	if e == nil {
		t.Fatal("誤鍵を許可")
	}
	var strict *strictClose
	select {
	case strict = <-done:
	case <-time.After(time.Second):
		t.Fatal("認証拒否が終了しない")
	}
	if strict == nil {
		t.Fatal("接続なし")
	}
	closeSession(t, s)
	if strict.calls.Load() != 1 || s.Snapshot().Owned != 0 {
		t.Fatal("下層を二重 close または参照が残る")
	}
}

func TestAuthenticationStageRevocation(t *testing.T) {
	for _, stage := range []string{"candidate", "verified"} {
		t.Run(stage, func(t *testing.T) {
			c, a := config(t, "restricted")
			var dials atomic.Int64
			s, e := newSession(c, func(context.Context) (net.Conn, error) { dials.Add(1); return nil, errors.New("試験で禁止") })
			if e != nil {
				t.Fatal(e)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			denied := make(chan bool, 1)
			if stage == "candidate" {
				original := s.config.PublicKeyCallback
				s.config.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
					close(entered)
					<-release
					p, e := original(meta, key)
					denied <- e != nil
					return p, e
				}
			} else {
				original := s.config.VerifiedPublicKeyCallback
				s.config.VerifiedPublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey, p *ssh.Permissions, algorithm string) (*ssh.Permissions, error) {
					close(entered)
					<-release
					p, e := original(meta, key, p, algorithm)
					denied <- e != nil
					return p, e
				}
			}
			address := start(t, s, c)
			t.Cleanup(func() { onceRelease(release) })
			done := make(chan error, 1)
			go func() {
				cl, e := client(address, c, a, "key")
				if cl != nil {
					cl.Close()
				}
				done <- e
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("認証段階に入らない")
			}
			s.Revoke(c.Identity)
			onceRelease(release)
			select {
			case refused := <-denied:
				if !refused {
					t.Fatal("失効後の認証callbackが許可")
				}
			case <-time.After(time.Second):
				t.Fatal("認証callbackが終了しない")
			}
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("失効中の認証が成功")
				}
			case <-time.After(time.Second):
				t.Fatal("認証が終了しない")
			}
			if dials.Load() != 0 {
				t.Fatal("認証中に転送")
			}
		})
	}
}

func TestCapacityAcrossTransports(t *testing.T) {
	f := fixture(t)
	c, a := config(t, "restricted")
	c.MaxChannels = 1
	s, e := newSession(c, func(ctx context.Context) (net.Conn, error) { return testDial(ctx, f.l.Addr().String()) })
	if e != nil {
		t.Fatal(e)
	}
	address := start(t, s, c)
	left, e := client(address, c, a, "key")
	if e != nil {
		t.Fatal(e)
	}
	defer left.Close()
	right, e := client(address, c, a, "key")
	if e != nil {
		t.Fatal(e)
	}
	defer right.Close()
	first, e := open(left, "127.0.0.1", 22)
	if e != nil {
		t.Fatal(e)
	}
	echo(t, first)
	other, e := open(right, "127.0.0.1", 22)
	if other != nil {
		other.Close()
	}
	if e == nil {
		t.Fatal("別transportで上限回避")
	}
	first.Close()
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	other, e = open(right, "127.0.0.1", 22)
	if e != nil {
		t.Fatal("正常close後の枠が戻らない")
	}
	defer other.Close()
	echo(t, other)
}

func TestMain(m *testing.M) {
	runtime.GOMAXPROCS(1)
	if os.Getenv("RESCUE_SESSION_FIXTURE_CHILD") == "1" {
		os.Exit(sessionFixtureChild())
	}
	if os.Getenv("RESCUE_OWNED_SOCKET_CHILD") == "1" {
		os.Exit(ownedSocketChild())
	}
	started := time.Now()
	code := m.Run()
	network.Lock()
	record := map[string]any{"owned_session_children": childEvidence, "fault_observations": faultEvidence, "independent_cutoff_observations": cutoffEvidence, "test_exit_code": code, "platform": runtime.GOOS, "architecture": runtime.GOARCH, "pid": os.Getpid(), "duration_ms": time.Since(started).Milliseconds(), "listener_addresses": network.history, "dial_addresses": network.dials, "remaining_listeners": len(network.listeners), "remaining_connections": len(network.conns), "forbidden_dial_attempts": network.forbidden, "production_target_22": "未接続。固定 wire 方針と独立したテスト専用 dial を使用"}
	if len(network.listeners) != 0 || len(network.conns) != 0 || network.forbidden != 0 {
		code = 1
	}
	record["test_exit_code"] = code
	network.Unlock()
	f, e := os.Create("go-result.json")
	if e != nil {
		os.Exit(2)
	}
	e = json.NewEncoder(f).Encode(record)
	f.Close()
	if e != nil {
		os.Exit(2)
	}
	os.Exit(code)
}
