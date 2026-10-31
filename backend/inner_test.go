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

// 内側SSHの試験はmemory duplexと合成鍵だけを使い、listener、dial、実shell、guest processを作らない。
type fakeShell struct {
	stdinR           *io.PipeReader
	stdinW           *io.PipeWriter
	stdoutR, stderrR *io.PipeReader
	stdoutW, stderrW *io.PipeWriter
	closes           atomic.Int32
	fail, panics     atomic.Bool
}

func newFakeShell() *fakeShell {
	f := &fakeShell{}
	f.stdinR, f.stdinW = io.Pipe()
	f.stdoutR, f.stdoutW = io.Pipe()
	f.stderrR, f.stderrW = io.Pipe()
	return f
}

func (f *fakeShell) Stdin() io.WriteCloser { return f.stdinW }
func (f *fakeShell) Stdout() io.Reader     { return f.stdoutR }
func (f *fakeShell) Stderr() io.Reader     { return f.stderrR }
func (f *fakeShell) Close(context.Context) error {
	f.closes.Add(1)
	if f.panics.Load() {
		panic("fixture close panic")
	}
	if f.fail.Load() {
		return errors.New("fixture close failure")
	}
	for _, c := range []io.Closer{f.stdinW, f.stdinR, f.stdoutR, f.stdoutW, f.stderrR, f.stderrW} {
		c.Close()
	}
	return nil
}

type nilStdinShell struct{ *fakeShell }

func (*nilStdinShell) Stdin() io.WriteCloser { return nil }

type countingSource struct{ accepts, closes atomic.Int32 }

func (s *countingSource) Accept(ctx context.Context) (net.Conn, error) {
	s.accepts.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *countingSource) Close(context.Context) error { s.closes.Add(1); return nil }

// 外側transportにも同じmemory duplexを使う。ownerなしの組は内側endpointとして受理されない。
func memoryConn() (net.Conn, net.Conn) { d := newOwnedDuplex(nil); return d.client, d.server }

func ownedSession(t *testing.T, mode string, extra ...ssh.Signer) (*Session, Config, ssh.Signer, *fakeShell) {
	t.Helper()
	c, signer := config(t, mode)
	for _, k := range extra {
		c.AllowedKeys = append(c.AllowedKeys, k.PublicKey())
	}
	s, err := NewOwned(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, s) })
	var shell *fakeShell
	if mode == "full" {
		shell = newFakeShell()
		if err := s.AttachOwned(nil, shell, time.Now().Add(5*time.Second), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if s.Ready(c.Identity) != nil || s.Activate(c.Identity) != nil {
		t.Fatal("所有Sessionを開始できません")
	}
	return s, c, signer, shell
}

func sshClient(t *testing.T, conn net.Conn, c Config, signer ssh.Signer, user string) (*ssh.Client, error) {
	t.Helper()
	cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(c.HostKey.PublicKey())}
	sc, chans, reqs, err := ssh.NewClientConn(conn, "rescue-owned", cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	client := ssh.NewClient(sc, chans, reqs)
	t.Cleanup(func() { client.Close() })
	return client, nil
}

func outerClient(t *testing.T, s *Session, c Config, signer ssh.Signer) *ssh.Client {
	t.Helper()
	clientEnd, serverEnd := memoryConn()
	go func() {
		if errors.Is(s.Serve(serverEnd), ErrNotAccepted) {
			serverEnd.Close()
		}
	}()
	client, err := sshClient(t, clientEnd, c, signer, c.Identity.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// wireのliteralだけを使う。物理宛先はSession内の所有endpointである。
func innerClient(t *testing.T, outer *ssh.Client, c Config, signer ssh.Signer, user string) (*ssh.Client, error) {
	t.Helper()
	target, err := outer.Dial("tcp", "127.0.0.1:22")
	if err != nil {
		return nil, err
	}
	return sshClient(t, target, c, signer, user)
}

func readWithin(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	return within(t, func() ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(r, b); return b, err })
}

func readAllWithin(t *testing.T, r io.Reader) string {
	t.Helper()
	return within(t, func() ([]byte, error) { return io.ReadAll(r) })
}

func within(t *testing.T, read func() ([]byte, error)) string {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() { data, err := read(); done <- result{data, err} }()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return string(got.data)
	case <-time.After(2 * time.Second):
		t.Fatal("memory streamの読取りが2秒以内に終わりません")
	}
	return ""
}

func dialCount() int { network.Lock(); defer network.Unlock(); return len(network.dials) }

func TestOwnedShellRoundTripOverWireLiteralWithoutTCPDial(t *testing.T) {
	dials := dialCount()
	s, c, signer, shell := ownedSession(t, "full")
	if s.dial != nil || s.inner == nil {
		t.Fatal("所有Sessionが固定TCP dialを保持しています")
	}
	inner, err := innerClient(t, outerClient(t, s, c, signer), c, signer, c.Identity.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inner.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	stderr, _ := session.StderrPipe()
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte("Get-Date\n")); err != nil {
		t.Fatal(err)
	}
	if got := readWithin(t, shell.stdinR, 9); got != "Get-Date\n" {
		t.Fatalf("guest stdinへ届いていません: %q", got)
	}
	go shell.stdoutW.Write([]byte("out\n"))
	go shell.stderrW.Write([]byte("err\n"))
	if got := readWithin(t, stdout, 4); got != "out\n" {
		t.Fatalf("stdout: %q", got)
	}
	if got := readWithin(t, stderr, 4); got != "err\n" {
		t.Fatalf("stderr: %q", got)
	}
	if snap := s.Snapshot(); dialCount() != dials || snap.ShellPending != 1 || snap.Forwarding != 2 {
		t.Fatalf("TCP dialまたは所有状態が不正: dials=%d snapshot=%+v", dialCount()-dials, snap)
	}
}

func TestOwnedShellHalfCloseBothDirections(t *testing.T) {
	s, c, signer, shell := ownedSession(t, "full")
	inner, err := innerClient(t, outerClient(t, s, c, signer), c, signer, c.Identity.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inner.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	readWithin(t, shell.stdinR, 5)
	// client EOFはguest stdinだけを閉じ、出力方向は継続する。
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if rest := readAllWithin(t, shell.stdinR); rest != "" {
		t.Fatalf("EOF後の入力: %q", rest)
	}
	if shell.closes.Load() != 0 {
		t.Fatal("入力側の半閉鎖でshell全体を閉じました")
	}
	go func() { shell.stdoutW.Write([]byte("bye\n")); shell.stdoutW.Close(); shell.stderrW.Close() }()
	if got := readAllWithin(t, stdout); got != "bye\n" {
		t.Fatalf("guest出力のEOF前データ: %q", got)
	}
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.ShellPending == 0 })
	if shell.closes.Load() != 1 {
		t.Fatalf("shell Close回数: %d", shell.closes.Load())
	}
}

func TestOwnedShellRejectsArbitraryDestinationAndMalformedPayload(t *testing.T) {
	dials := dialCount()
	s, c, signer, _ := ownedSession(t, "full")
	outer := outerClient(t, s, c, signer)
	for _, address := range []string{"127.0.0.2:22", "127.0.0.1:2222", "localhost:22", "[::1]:22", "0.0.0.0:22", "127.000.000.001:22"} {
		if conn, err := outer.Dial("tcp", address); err == nil {
			conn.Close()
			t.Fatalf("literal以外の宛先を受理: %s", address)
		}
	}
	literal := ssh.Marshal(struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}{"127.0.0.1", 22, "0.0.0.0", 0})
	for _, open := range []struct {
		kind    string
		payload []byte
	}{{"direct-tcpip", nil}, {"direct-tcpip", []byte{1, 2, 3}}, {"direct-tcpip", append(literal, 0)}, {"session", nil}, {"forwarded-tcpip", literal}, {"direct-streamlocal@openssh.com", nil}} {
		if ch, reqs, err := outer.OpenChannel(open.kind, open.payload); err == nil {
			go ssh.DiscardRequests(reqs)
			ch.Close()
			t.Fatalf("不正な外側channelを受理: %s", open.kind)
		}
	}
	s.mu.Lock()
	used := s.inner.transportUsed
	s.mu.Unlock()
	if used {
		t.Fatal("拒否した要求が単回の内側endpointを消費しました")
	}
	if _, err := innerClient(t, outer, c, signer, c.Identity.LaunchID); err != nil {
		t.Fatal(err)
	}
	if dialCount() != dials {
		t.Fatal("所有endpointの代わりにTCP dialしました")
	}
}

func TestOwnedShellRejectsInnerKeyOrUserDifferentFromOuter(t *testing.T) {
	for _, name := range []string{"key", "user"} {
		t.Run(name, func(t *testing.T) {
			second := key(t)
			s, c, signer, shell := ownedSession(t, "full", second)
			outer := outerClient(t, s, c, signer)
			innerSigner, user := second, c.Identity.LaunchID
			if name == "user" {
				innerSigner, user = signer, "other-launch"
			}
			// 鍵集合内の別鍵でも、外側で署名検証した鍵と異なれば内側認証しない。
			if inner, err := innerClient(t, outer, c, innerSigner, user); err == nil {
				inner.Close()
				t.Fatal("外側と異なる内側認証を受理しました")
			}
			if inner, err := innerClient(t, outer, c, signer, c.Identity.LaunchID); err == nil {
				inner.Close()
				t.Fatal("失敗後に単回endpointを再利用しました")
			}
			if shell.closes.Load() != 0 || s.Snapshot().ShellPending != 1 {
				t.Fatal("認証失敗でshellを開始または閉鎖しました")
			}
		})
	}
}

func TestOwnedShellRejectsPtyExecSubsystemEnvForwardAgentX11(t *testing.T) {
	s, c, signer, shell := ownedSession(t, "full")
	inner, err := innerClient(t, outerClient(t, s, c, signer), c, signer, c.Identity.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, err := inner.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Address string
		Port    uint32
	}{"127.0.0.1", 0})); err != nil || ok {
		t.Fatal("内側の逆転送を受理しました")
	}
	for _, kind := range []string{"direct-tcpip", "forwarded-tcpip", "x11", "auth-agent@openssh.com"} {
		if ch, reqs, err := inner.OpenChannel(kind, nil); err == nil {
			go ssh.DiscardRequests(reqs)
			ch.Close()
			t.Fatalf("内側channelを受理: %s", kind)
		}
	}
	ch, reqs, err := inner.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	for _, request := range []struct {
		kind    string
		payload []byte
	}{
		{"pty-req", ssh.Marshal(struct {
			Term                         string
			Columns, Rows, Width, Height uint32
			Modes                        string
		}{"xterm", 80, 24, 0, 0, ""})},
		{"exec", ssh.Marshal(struct{ Command string }{"whoami"})},
		{"subsystem", ssh.Marshal(struct{ Name string }{"sftp"})},
		{"env", ssh.Marshal(struct{ Name, Value string }{"PATH", "C:\\"})},
		{"x11-req", ssh.Marshal(struct {
			Single           bool
			Protocol, Cookie string
			Screen           uint32
		}{false, "MIT-MAGIC-COOKIE-1", "00", 0})},
		{"auth-agent-req@openssh.com", nil},
		{"window-change", ssh.Marshal(struct{ Columns, Rows, Width, Height uint32 }{80, 24, 0, 0})},
		{"signal", ssh.Marshal(struct{ Signal string }{"KILL"})},
		{"shell", []byte{0}},
	} {
		if ok, err := ch.SendRequest(request.kind, true, request.payload); err != nil || ok {
			t.Fatalf("内側要求を受理: %s %v", request.kind, err)
		}
	}
	s.mu.Lock()
	started := s.inner.shellUsed
	s.mu.Unlock()
	if started || shell.closes.Load() != 0 {
		t.Fatal("拒否要求でshellを開始しました")
	}
}

func TestOwnedShellSingleTransportSessionAndShell(t *testing.T) {
	s, c, signer, shell := ownedSession(t, "full")
	outer := outerClient(t, s, c, signer)
	inner, err := innerClient(t, outer, c, signer, c.Identity.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := innerClient(t, outer, c, signer, c.Identity.LaunchID); err == nil {
		second.Close()
		t.Fatal("2本目の内側transportを受理しました")
	}
	ch, reqs, err := inner.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if ok, err := ch.SendRequest("shell", true, nil); err != nil || !ok {
		t.Fatalf("shellを開始できません: %v", err)
	}
	if ok, err := ch.SendRequest("shell", true, nil); err != nil || ok {
		t.Fatal("2回目のshellを受理しました")
	}
	if extra, r, err := inner.OpenChannel("session", nil); err == nil {
		go ssh.DiscardRequests(r)
		extra.Close()
		t.Fatal("2本目のsessionを受理しました")
	}
	ch.Close()
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.ShellPending == 0 })
	if extra, r, err := inner.OpenChannel("session", nil); err == nil {
		go ssh.DiscardRequests(r)
		extra.Close()
		t.Fatal("shell終了後に同じ承認で新しいsessionを受理しました")
	}
	if shell.closes.Load() != 1 {
		t.Fatalf("shell Close回数: %d", shell.closes.Load())
	}
}

func TestOwnedShellRevokeClosesInnerDuplexAndStreamsOnce(t *testing.T) {
	s, c, signer, shell := ownedSession(t, "full")
	inner, err := innerClient(t, outerClient(t, s, c, signer), c, signer, c.Identity.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inner.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := session.StdinPipe()
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	readWithin(t, shell.stdinR, 2)
	waited := make(chan error, 1)
	go func() { waited <- session.Wait() }()
	if err := s.Revoke(c.Identity); err != nil {
		t.Fatal(err)
	}
	closeSession(t, s)
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("失効後も内側shell channelが残っています")
	}
	if snap := s.Snapshot(); snap.State != "terminated" || snap.Owned != 0 || snap.ShellPending != 0 || snap.Forwarding != 0 || shell.closes.Load() != 1 {
		t.Fatalf("失効回収が不完全: %+v closes=%d", snap, shell.closes.Load())
	}
	closeSession(t, s)
	if shell.closes.Load() != 1 {
		t.Fatal("確認済みshellを再Closeしました")
	}
	if _, err := shell.stdinR.Read(make([]byte, 1)); err == nil {
		t.Fatal("失効後もguest stdinが開いています")
	}
}

func TestOwnedShellUnconfirmedStreamCloseRetainsReferences(t *testing.T) {
	for _, name := range []string{"error", "panic"} {
		t.Run(name, func(t *testing.T) {
			s, _, _, shell := ownedSession(t, "full")
			if name == "error" {
				shell.fail.Store(true)
			} else {
				shell.panics.Store(true)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.Close(ctx); !errors.Is(err, ErrUnconfirmed) {
				t.Fatalf("未確認shell closeを成功扱い: %v", err)
			}
			snap := s.Snapshot()
			s.mu.Lock()
			retained := s.inner.shell != nil && s.inner.shell.owner == ShellStreams(shell)
			s.mu.Unlock()
			attempts := shell.closes.Load()
			if snap.State != "revoking" || snap.ShellPending != 1 || snap.ShellCloseFailures != 1 || !retained || attempts < 1 {
				t.Fatalf("参照または失効状態を失いました: %+v retained=%v", snap, retained)
			}
			shell.fail.Store(false)
			shell.panics.Store(false)
			closeSession(t, s)
			if snap := s.Snapshot(); snap.State != "terminated" || snap.ShellPending != 0 || shell.closes.Load() <= attempts {
				t.Fatalf("同じ参照の再試行で終了確認できません: %+v", snap)
			}
		})
	}
}

func TestRestrictedInnerRejectsAllUntilHelpersDefined(t *testing.T) {
	c, signer := config(t, "restricted")
	s, err := NewOwned(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, s) })
	shell := newFakeShell()
	if err := s.AttachOwned(nil, shell, time.Now().Add(5*time.Second), time.Second); !errors.Is(err, ErrInvalidConfig) || shell.closes.Load() != 0 {
		t.Fatalf("restrictedへshellを移しました: %v", err)
	}
	if s.Ready(c.Identity) != nil || s.Activate(c.Identity) != nil {
		t.Fatal("restrictedを開始できません")
	}
	inner, err := innerClient(t, outerClient(t, s, c, signer), c, signer, c.Identity.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	if session, err := inner.NewSession(); err == nil {
		session.Close()
		t.Fatal("helper未定義のrestrictedでsessionを開きました")
	}
}

func TestOwnedFullRefusesActivationWithoutShellAndHasNoFixedDial(t *testing.T) {
	c, _ := config(t, "full")
	s, err := NewOwned(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, s) })
	if s.dial != nil || s.inner == nil {
		t.Fatal("所有Sessionが固定TCP dialを保持しています")
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := s.Activate(c.Identity); !errors.Is(err, ErrNotAccepted) || s.Snapshot().State != "ready" {
		t.Fatalf("shell未移転のfullを開始しました: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.ActivateForStart(ctx, c.Identity, time.Now().Add(time.Second)); !errors.Is(err, ErrNotAccepted) || s.Snapshot().State != "revoking" {
		t.Fatalf("shell未移転の開始要求を失効しません: %v", err)
	}
	legacy, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSession(t, legacy)
	if legacy.inner != nil || legacy.dial == nil {
		t.Fatal("旧評価経路と所有経路が混ざりました")
	}
}

func TestOwnedEndpointRejectsForeignDuplexAndSecondAuthority(t *testing.T) {
	a, _, signer, shell := ownedSession(t, "full")
	b, _, _, _ := ownedSession(t, "full")
	key := signer.PublicKey().Marshal()
	foreign := newOwnedDuplex(a.inner)
	own := newOwnedDuplex(b.inner)
	b.mu.Lock()
	foreignErr := b.startInnerLocked(foreign.server, key)
	clientErr := b.startInnerLocked(own.client, key)
	unowned := b.startInnerLocked(newOwnedDuplex(nil).server, key)
	used := b.inner.transportUsed
	b.mu.Unlock()
	if !errors.Is(foreignErr, ErrNotAccepted) || !errors.Is(clientErr, ErrNotAccepted) || !errors.Is(unowned, ErrNotAccepted) || used || b.Snapshot().Owned != 0 {
		t.Fatal("別owner/方向のduplexを内側endpointへ受理しました")
	}
	// 同じguest shellを別Authorityへ再移転しない。
	oc, _ := config(t, "full")
	other, err := NewOwned(oc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, other) })
	if err := other.AttachOwned(nil, shell, time.Now().Add(5*time.Second), time.Second); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("所有済みshellを別Sessionへ移しました: %v", err)
	}
	other.mu.Lock()
	attached := other.inner.shell != nil
	other.mu.Unlock()
	if attached || shell.closes.Load() != 0 {
		t.Fatal("拒否した移転がshellを変更しました")
	}
}

func TestOwnedInnerAuthRejectedAfterRevokeOrExpiry(t *testing.T) {
	for _, name := range []string{"revoked", "expired"} {
		t.Run(name, func(t *testing.T) {
			c, signer := config(t, "full")
			if name == "expired" {
				c.LeaseDuration = 500 * time.Millisecond
			}
			s, err := NewOwned(c)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeSession(t, s) })
			shell := newFakeShell()
			if s.AttachOwned(nil, shell, time.Now().Add(5*time.Second), time.Second) != nil || s.Ready(c.Identity) != nil || s.Activate(c.Identity) != nil {
				t.Fatal("所有Sessionを開始できません")
			}
			target, err := outerClient(t, s, c, signer).Dial("tcp", "127.0.0.1:22")
			if err != nil {
				t.Fatal(err)
			}
			if name == "revoked" {
				if err := s.Revoke(c.Identity); err != nil {
					t.Fatal(err)
				}
			} else {
				awaitSnapshot(t, s, func(x Snapshot) bool { return x.Reason == "lease-expired" })
			}
			if inner, err := sshClient(t, target, c, signer, c.Identity.LaunchID); err == nil {
				inner.Close()
				t.Fatal("失効後に内側認証しました")
			}
			closeSession(t, s)
			if s.Snapshot().ShellPending != 0 || shell.closes.Load() != 1 {
				t.Fatalf("失効後のshell回収: closes=%d", shell.closes.Load())
			}
		})
	}
}

func TestOwnedAttachIsAllOrNothing(t *testing.T) {
	c, _ := config(t, "full")
	s, err := NewOwned(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, s) })
	deadline := time.Now().Add(5 * time.Second)
	source := &countingSource{}
	var typedNil *fakeShell
	for _, shell := range []ShellStreams{&nilStdinShell{newFakeShell()}, nil, typedNil} {
		if err := s.AttachOwned(source, shell, deadline, time.Second); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("不正shellで移転しました: %v", err)
		}
	}
	if err := s.AttachAcceptedSource(&countingSource{}, deadline, time.Second); err != nil {
		t.Fatal(err)
	}
	shell := newFakeShell()
	if err := s.AttachOwned(source, shell, deadline, time.Second); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("source移転不能時にshellだけ移しました: %v", err)
	}
	s.mu.Lock()
	attached := s.inner.shell != nil
	s.mu.Unlock()
	if attached || source.accepts.Load() != 0 || source.closes.Load() != 0 || shell.closes.Load() != 0 {
		t.Fatal("拒否した移転がcaller所有物へ触れました")
	}
	// 拒否されたshellは未登録のまま、別のprepared Sessionへ一度だけ移せる。
	nc, _ := config(t, "full")
	next, err := NewOwned(nc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, next) })
	if err := next.AttachOwned(nil, shell, deadline, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := next.AttachOwned(nil, newFakeShell(), deadline, time.Second); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("2個目のshellを移しました: %v", err)
	}
	rc, _ := config(t, "restricted")
	restricted, err := NewOwned(rc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, restricted) })
	if err := restricted.Ready(rc.Identity); err != nil {
		t.Fatal(err)
	}
	late := &countingSource{}
	if err := restricted.AttachOwned(late, nil, deadline, time.Second); !errors.Is(err, ErrNotAccepted) || late.accepts.Load() != 0 || late.closes.Load() != 0 {
		t.Fatalf("prepared以外で移転しました: %v", err)
	}
}
