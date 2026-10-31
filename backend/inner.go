package backend

import (
	"bytes"
	"context"
	"io"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/auth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease"
	"golang.org/x/crypto/ssh"
)

// ShellStreams はfull modeの唯一のguest shellのstdio端を保護bootstrapからSessionへ移す所有境界。
// 実装はguestのprocess、token、Job、制御pipe、host秘密鍵を含めない。
// どのmethodの戻り値も認証・認可・所有者確認にならない。stdinへ書ける相手は同じSessionの
// Authority、起動時の鍵集合、外側と同一の署名検証済み鍵だけで決まる。
// 3つのstreamは移転時に一度だけ取得し、以後のmethod呼出しで差し替えられない。
// Closeのnilは3つのstream端の閉鎖確認であり、guest processやJobの終了・回収の証明ではない。
type ShellStreams interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Close(context.Context) error
}

type shellStreams struct {
	stdin          io.WriteCloser
	stdout, stderr io.Reader
}

// ownedShell の状態はSession.muが保護する。失敗後も参照を保持して再試行する。
type ownedShell struct {
	owner                   ShellStreams
	streams                 shellStreams
	cleanup                 time.Duration
	closing, closed, failed bool
}

// innerEndpoint はwireのliteral 127.0.0.1:22を受理した外側channelの唯一の転送先。
// TCP、stock sshd、既存OpenSSHへ接続せず、同じSessionのAuthority・鍵集合・host signerを使う。
type innerEndpoint struct {
	keys                                  *auth.Keys
	host                                  ssh.Signer
	full                                  bool
	shell                                 *ownedShell
	transportUsed, sessionUsed, shellUsed bool // 承認ごとに各1回だけ。再利用しない。
}

// 同じShellStreams objectを別Sessionへ再び移さない。process内の取り違え検出であり認証ではない。
var shellOwners = struct {
	sync.Mutex
	used map[ShellStreams]struct{}
}{used: map[ShellStreams]struct{}{}}

// NewOwned は製品所有の内側SSH endpointだけへ転送するSessionを作る。固定TCP dialを持たない。
// fullはAttachOwnedでshellを受け取るまで開始できない。restrictedのhelperは未定義のため内側で全て拒否する。
func NewOwned(config Config) (*Session, error) { return newSessionCore(config, nil, true) }

// AttachOwned はprepared中の同じNewOwned Sessionへ、受理sourceとfullの内側shellを一度だけ移す。
// 全検査を移転前に行い、errorなら両方ともcallerの所有に残る。sourceはnil可、restrictedはshellを拒否する。
func (s *Session) AttachOwned(source AcceptedSource, shell ShellStreams, hardDeadline time.Time, cleanupTimeout time.Duration) error {
	if s == nil || s.authority == nil || s.inner == nil || (source == nil && shell == nil) || (shell != nil) != s.inner.full || cleanupTimeout <= 0 || hardDeadline.IsZero() || !time.Now().Before(hardDeadline) {
		return ErrInvalidConfig
	}
	var streams shellStreams
	if shell != nil {
		var err error
		if streams, err = takeShellStreams(shell); err != nil {
			return err
		}
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if source != nil {
		ctx, cancel = context.WithDeadline(s.authority.Context(), hardDeadline)
	}
	keep := false
	defer func() {
		if !keep && cancel != nil {
			cancel()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.inner.shell != nil || (source != nil && s.accepted != nil) || s.authority.Snapshot().State != lease.Prepared {
		return ErrNotAccepted
	}
	if shell != nil {
		shellOwners.Lock()
		_, used := shellOwners.used[shell]
		if !used {
			shellOwners.used[shell] = struct{}{}
		}
		shellOwners.Unlock()
		if used {
			return ErrNotAccepted
		}
		s.inner.shell = &ownedShell{owner: shell, streams: streams, cleanup: cleanupTimeout}
	}
	if source != nil {
		s.attachSourceLocked(source, ctx, cancel, cleanupTimeout)
	}
	keep = true
	return nil
}

// 所有objectのpointerだけを受け付け、stream取得のpanicは移転前の拒否にする。
func takeShellStreams(shell ShellStreams) (out shellStreams, err error) {
	value := reflect.ValueOf(shell)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return shellStreams{}, ErrInvalidConfig
	}
	defer func() {
		if recover() != nil {
			out, err = shellStreams{}, ErrInvalidConfig
		}
	}()
	out = shellStreams{stdin: shell.Stdin(), stdout: shell.Stdout(), stderr: shell.Stderr()}
	if out.stdin == nil || out.stdout == nil || out.stderr == nil {
		return shellStreams{}, ErrInvalidConfig
	}
	return out, nil
}

// fullの開始にはshell移転済みが必要。missing shellを固定TCPやexec専用へ縮退しない。
func (s *Session) innerReadyLocked() bool {
	return s.inner == nil || !s.inner.full || s.inner.shell != nil
}

// closeShellLocked はlock下で登録だけを行い、外部CloseはSession workerで期限付きに実行する。
// error、panic、期限切れでは失効し、参照を保持して次の停止要求で再試行する。
func (s *Session) closeShellLocked() {
	if s.inner == nil || s.inner.shell == nil {
		return
	}
	sh := s.inner.shell
	if sh.closing || sh.closed {
		return
	}
	sh.closing = true
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		ctx, cancel := context.WithTimeout(context.Background(), sh.cleanup)
		err := closeShellStreams(sh.owner, ctx)
		expired := ctx.Err() != nil
		cancel()
		if err != nil || expired {
			_ = s.Revoke(s.authority.Identity())
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		sh.closing = false
		sh.closed = err == nil && !expired
		sh.failed = !sh.closed
	}()
}

func (s *Session) closeShell() { s.mu.Lock(); defer s.mu.Unlock(); s.closeShellLocked() }

func closeShellStreams(shell ShellStreams, ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnconfirmed
		}
	}()
	return shell.Close(ctx)
}

// dialInner は外側forwardの転送先として、同じSessionが所有するduplexだけを返す。
func (s *Session) dialInner(key []byte) (net.Conn, func() error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inner == nil {
		return nil, nil, ErrNotAccepted
	}
	pair := newOwnedDuplex(s.inner)
	if err := s.startInnerLocked(pair.server, key); err != nil {
		return nil, nil, err
	}
	return pair.client, pair.cutoff, nil
}

// startInnerLocked は同じendpointが作ったserver端だけを、外側の署名検証済み鍵に束縛して1回だけ受理する。
func (s *Session) startInnerLocked(end *duplexEnd, key []byte) error {
	in := s.inner
	if in == nil || end == nil || end.pair == nil || end.pair.owner != in || end != end.pair.server || len(key) == 0 || in.transportUsed || s.closing || !s.authority.IsActive() {
		return ErrNotAccepted
	}
	in.transportUsed = true
	h := s.ownLocked(end)
	h.cutoff = end.pair.cutoff
	s.workers.Add(1)
	go s.serveInner(h, bytes.Clone(key))
	return nil
}

func (s *Session) serveInner(h *owned, key []byte) {
	defer s.workers.Done()
	defer s.closeOne(h)
	config, err := s.inner.keys.BoundServerConfig(s.inner.host, s.authority.IsActive, key)
	if err != nil || h.SetDeadline(time.Now().Add(2*time.Second)) != nil {
		return
	}
	conn, channels, requests, err := ssh.NewServerConn(h, config)
	if err != nil {
		return
	}
	requestsDone := make(chan struct{})
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer close(requestsDone)
		ssh.DiscardRequests(requests)
	}()
	defer func() {
		s.closeOne(h)
		for channel := range channels {
			channel.Reject(ssh.Prohibited, "接続を終了しています")
		}
		_ = conn.Wait()
		<-requestsDone
	}()
	got, ok := auth.AuthenticatedKey(conn.Permissions)
	if !ok || !bytes.Equal(got, key) || !s.authority.IsActive() || h.SetDeadline(time.Time{}) != nil {
		return
	}
	for channel := range channels {
		if !s.claimInnerSession(channel) {
			channel.Reject(ssh.Prohibited, "要求を拒否しました")
			continue
		}
		s.workers.Add(1)
		go s.innerSession(channel)
	}
}

// restrictedのhelper実体は未定義のため、session自体を開かない。
func (s *Session) claimInnerSession(channel ssh.NewChannel) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channel.ChannelType() != "session" || len(channel.ExtraData()) != 0 || !s.inner.full || s.inner.sessionUsed || s.closing || !s.authority.IsActive() {
		return false
	}
	s.inner.sessionUsed = true
	return true
}

func (s *Session) claimShell() (shellStreams, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.inner.shell
	if sh == nil || s.inner.shellUsed || sh.closing || sh.closed || s.closing || !s.authority.IsActive() {
		return shellStreams{}, false
	}
	s.inner.shellUsed = true
	return sh.streams, true
}

// 引数なしの"shell"を1回だけ許す。pty-req、exec、subsystem、env、x11、agent、window-change等は拒否する。
func (s *Session) innerSession(open ssh.NewChannel) {
	defer s.workers.Done()
	channel, requests, err := open.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	started := false
	for request := range requests {
		var streams shellStreams
		ok := request.Type == "shell" && len(request.Payload) == 0 && !started
		if ok {
			streams, ok = s.claimShell()
		}
		if request.WantReply {
			_ = request.Reply(ok, nil)
		}
		if ok {
			started = true
			s.workers.Add(1)
			go s.pumpShell(channel, streams)
		}
	}
	// channel終了後に戻らないguest出力を待たず、同じowner経路で閉じる。
	if started {
		s.closeShell()
	}
}

// 正常EOFは方向ごとに半閉鎖する。error/失効ではshellとchannelを閉じる。
func (s *Session) pumpShell(channel ssh.Channel, streams shellStreams) {
	defer s.workers.Done()
	s.mu.Lock()
	s.forwarding++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.forwarding--; s.mu.Unlock() }()
	stop := func() { s.closeShell(); channel.Close() }
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		_, err := io.Copy(authorityWriter{s.authority, streams.stdin}, channel)
		if err == nil {
			err = streams.stdin.Close()
		}
		if err != nil {
			stop()
		}
	}()
	stderrDone := make(chan error, 1)
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		_, err := io.Copy(authorityWriter{s.authority, channel.Stderr()}, streams.stderr)
		stderrDone <- err
	}()
	_, err := io.Copy(authorityWriter{s.authority, channel}, streams.stdout)
	if err != nil {
		stop()
	}
	if e := <-stderrDone; e != nil && err == nil {
		err = e
		stop()
	}
	if err == nil {
		_ = channel.CloseWrite()
	}
	// guest出力の終端後はshellを再利用せず閉じる。exit codeは受け取らないため送らない。
	stop()
}
