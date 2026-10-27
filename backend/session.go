// Package backend は未配線の SSH バックエンド基盤である。
// 保護 supervisor や relay を提供せず、製品の開始拒否を解除しない。
package backend

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/auth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/forward"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease"
	"golang.org/x/crypto/ssh"
)

type Identity = lease.Identity

var (
	ErrNotAccepted   = errors.New("接続を受理しませんでした。呼び出し元が接続を閉じてください")
	ErrUnconfirmed   = errors.New("所有対象の終了を確認できません")
	ErrInvalidConfig = errors.New("バックエンド設定が不正です")
)

// Config に宛先、任意 dial、fixture の選択肢は存在しない。
// 呼び出し元が秘密鍵と識別子の生成、保護、再利用防止を担う。
type Config struct {
	Identity                       Identity
	OperationMode                  string
	AllowedKeys                    []ssh.PublicKey
	HostKey                        ssh.Signer
	MaximumDuration, LeaseDuration time.Duration
	MaxChannels, MaxHandshakes     int
	MaxTransports                  int // 0は既定8、明示値は1..64。認証中と認証済の合計。
}

type Snapshot struct {
	CutoffPending, CutoffFailures                              int
	Transports                                                 int
	Forwarding                                                 int // I/O処理数。未確認ownerが保持するActive容量とは別。
	State, Reason                                              string
	Identity                                                   Identity
	Pending, Active, Handshakes, Owned, Closing, CloseFailures int
}

// SSH ライブラリ自身の Close も同じ所有ハンドルへ集約する。
type owned struct {
	net.Conn
	closeMu                                sync.Mutex
	closed                                 bool
	closing                                bool
	failed                                 bool
	cutoff                                 func() error
	cutting, cut, cutFailed, adapterClosed bool               // Session.mu が保護する独立停止経路
	transport                              bool               // 生の受理済SSH接続。転送先は含めない。
	reservation                            *lease.Reservation // 両停止経路を確認するまで容量を保持する。
	channelPending                         bool               // SSH channel の終了待ちも同じ容量で所有する。
	onFailure                              func()
}

func (h *owned) Close() error {
	h.closeMu.Lock()
	if h.closed {
		h.closeMu.Unlock()
		return nil
	}
	err := h.Conn.Close()
	if err == nil {
		h.closed = true
	}
	h.closeMu.Unlock()
	if err != nil {
		h.onFailure()
	}
	return err
}

// Session は listener を作らない。渡された受理済み接続と転送先だけを所有する。
type Session struct {
	mu                        sync.Mutex
	workers                   *activity
	authority                 *lease.Authority
	policy                    forward.Policy
	config                    *ssh.ServerConfig
	dial                      targetDial
	owners                    map[*owned]struct{}
	closing                   bool
	handshakes, maxHandshakes int
	transports, maxTransports int
	forwarding                int
}

// PolicyDigest は既存 Python の不変方針と照合するための値を返す。
func PolicyDigest(mode string) (string, error) {
	p, e := forward.NewPolicy(mode)
	if e != nil {
		return "", e
	}
	return p.Digest(), nil
}

// New は固定 tcp4/127.0.0.1:22 を使う。呼ぶだけでは接続も待受も始まらない。
func New(config Config) (*Session, error) {
	return newSessionWithCutoff(config, func(ctx context.Context) (net.Conn, func() error, error) {
		raw, err := forward.DialFixed(ctx)
		if err != nil {
			return nil, nil, err
		}
		// 生 TCP の参照をアダプターとは独立に捕捉する。任意 Conn の展開は行わない。
		tcp, ok := raw.(*net.TCPConn)
		if !ok {
			raw.Close()
			return nil, nil, ErrInvalidConfig
		}
		return &tcpStream{tcp}, tcpCutoff(tcp), nil
	})
}

// tcpStream は実 TCP 本人の既閉だけを冪等化する。任意 adapter のエラーには適用しない。
type tcpStream struct{ *net.TCPConn }

func (c *tcpStream) Close() error { return tcpCutoff(c.TCPConn)() }

type targetDial func(context.Context) (net.Conn, func() error, error)

// tcpCutoff は実 TCP のハンドルだけを閉じる。アダプターの Close を呼ばない。
// net.ErrClosed はこの特定ハンドルが既に閉じられた場合だけ成功に含める。
func tcpCutoff(tcp *net.TCPConn) func() error {
	return func() error {
		err := tcp.Close()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

// 非公開の差替え点は同パッケージの _test.go だけで使用する。
func newSession(c Config, dial func(context.Context) (net.Conn, error)) (*Session, error) {
	if dial == nil {
		return nil, ErrInvalidConfig
	}
	return newSessionWithCutoff(c, func(ctx context.Context) (net.Conn, func() error, error) {
		conn, err := dial(ctx)
		return conn, nil, err
	})
}

func newSessionWithCutoff(c Config, dial targetDial) (*Session, error) {
	p, e := forward.NewPolicy(c.OperationMode)
	if c.MaxTransports == 0 {
		c.MaxTransports = 8
	}
	if c.MaxTransports < 1 || c.MaxTransports > 64 || e != nil || p.Digest() != c.Identity.PolicyDigest || c.MaxHandshakes < 1 || c.MaxHandshakes > 64 || dial == nil {
		return nil, ErrInvalidConfig
	}
	keys, e := auth.New(c.Identity.LaunchID, c.AllowedKeys)
	if e != nil {
		return nil, e
	}
	a, e := lease.New(c.Identity, c.MaximumDuration, c.LeaseDuration, c.MaxChannels)
	if e != nil {
		return nil, e
	}
	server, e := keys.ServerConfig(c.HostKey, a.IsActive)
	if e != nil {
		a.Revoke(c.Identity)
		<-a.Done()
		return nil, e
	}
	s := &Session{authority: a, policy: p, config: server, dial: dial, owners: make(map[*owned]struct{}), workers: newActivity(), maxHandshakes: c.MaxHandshakes, maxTransports: c.MaxTransports}
	s.workers.Add(1)
	go func() { defer s.workers.Done(); <-a.Context().Done(); s.closeOwned() }()
	return s, nil
}

func (s *Session) Ready(id Identity) error {
	return s.authority.Transition(id, lease.Prepared, lease.Ready)
}
func (s *Session) Activate(id Identity) error {
	return s.authority.Transition(id, lease.Ready, lease.Active)
}
func (s *Session) Renew(id Identity, sequence uint64) error { return s.authority.Renew(id, sequence) }
func (s *Session) Revoke(id Identity) error                 { return s.authority.Revoke(id) }

func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.authority.Snapshot()
	r := Snapshot{State: string(a.State), Reason: a.Reason, Identity: a.Identity, Pending: a.Pending, Active: a.Active, Handshakes: s.handshakes, Transports: s.transports, Forwarding: s.forwarding, Owned: len(s.owners)}
	for h := range s.owners {
		if h.closing {
			r.Closing++
		}
		if h.cutoff != nil && !h.cut {
			r.CutoffPending++
		}
		if h.cutFailed {
			r.CutoffFailures++
		}
		if h.failed {
			r.CloseFailures++
		}
	}
	return r
}

func (s *Session) ownLocked(c net.Conn) *owned {
	h := &owned{Conn: c, onFailure: func() { s.authority.Revoke(s.authority.Identity()) }}
	s.owners[h] = struct{}{}
	return h
}

// Close を lock 内で呼ばない。失敗した対象と進行中の試行を失わない。
func (s *Session) closeOneLocked(h *owned) {
	if _, ok := s.owners[h]; !ok {
		return
	}
	// 同じ停止要求の別 goroutine として開始する。アダプター lock を待たない。
	if h.cutoff != nil && !h.cut && !h.cutting {
		h.cutting = true
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			err := h.cutoff()
			if err != nil {
				s.authority.Revoke(s.authority.Identity())
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			h.cutting, h.cutFailed = false, err != nil
			h.cut = err == nil
			s.releaseClosedLocked(h)
		}()
	}
	if h.closing {
		return
	}
	h.closing = true
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		err := h.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		h.closing = false
		h.failed = err != nil
		h.adapterClosed = err == nil
		s.releaseClosedLocked(h)
	}()
}

func (s *Session) releaseClosedLocked(h *owned) {
	if h.adapterClosed && (h.cutoff == nil || h.cut) && !h.channelPending {
		if _, ok := s.owners[h]; ok {
			delete(s.owners, h)
			if h.transport {
				s.transports--
			}
			if h.reservation != nil {
				h.reservation.Release()
			}
		}
	}
}

func (s *Session) closeOne(h *owned) { s.mu.Lock(); defer s.mu.Unlock(); s.closeOneLocked(h) }
func (s *Session) finishChannel(h *owned) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h.channelPending = false
	s.releaseClosedLocked(h)
}
func (s *Session) closeOwned() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closing = true
	for h := range s.owners {
		s.closeOneLocked(h)
	}
}

// Serve の ErrNotAccepted だけは所有を移さない。呼び出し元が conn を閉じる。
// それ以外は接続を所有し、戻った後も終了失敗の参照を Close で再確認できる。
func (s *Session) Serve(conn net.Conn) error {
	// 実TCP本人だけを捕捉する。任意wrapperの内部参照や別socketを探索しない。
	if raw, ok := conn.(*net.TCPConn); ok {
		if raw == nil {
			return ErrNotAccepted
		}
		return s.serve(&tcpStream{raw}, tcpCutoff(raw))
	}
	return s.serve(conn, nil)
}

// 任意adapterの独立停止経路は公開設定から注入できない。
// 実TCPでない公開入力は通常Closeだけであり、物理強制停止を保証しない。
func (s *Session) serve(conn net.Conn, cutoff func() error) error {
	if conn == nil {
		return ErrNotAccepted
	}
	s.mu.Lock()
	if s.closing || !s.authority.IsActive() || s.handshakes >= s.maxHandshakes || s.transports >= s.maxTransports {
		s.mu.Unlock()
		return ErrNotAccepted
	}
	h := s.ownLocked(conn)
	h.cutoff = cutoff
	h.transport = true
	s.transports++
	s.handshakes++
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	defer s.closeOne(h)
	handshakeCounted := true
	defer func() {
		if handshakeCounted {
			s.mu.Lock()
			s.handshakes--
			s.mu.Unlock()
		}
	}()
	if e := conn.SetDeadline(time.Now().Add(2 * time.Second)); e != nil {
		return e
	}
	transport, channels, requests, e := ssh.NewServerConn(h, s.config)
	if e != nil {
		return e
	}
	s.mu.Lock()
	s.handshakes--
	handshakeCounted = false
	active := !s.closing && s.authority.IsActive()
	s.mu.Unlock()
	if !active {
		return lease.ErrRejected
	}
	if e = conn.SetDeadline(time.Time{}); e != nil {
		return e
	}
	s.workers.Add(1)
	go func() { defer s.workers.Done(); ssh.DiscardRequests(requests) }()
	for channel := range channels {
		if !s.policy.Permit(channel.ChannelType(), channel.ExtraData()) {
			channel.Reject(ssh.Prohibited, "要求を拒否しました")
			continue
		}
		reservation, e := s.authority.Reserve()
		if e != nil {
			channel.Reject(ssh.Prohibited, "利用できません")
			continue
		}
		s.workers.Add(1)
		go s.forward(channel, reservation)
	}
	// transport 自体の close は所有する raw 接続の close と同じ資源を使う。
	_ = transport
	return nil
}

// 各copyが読み取ったデータを書き込む前に認可を再確認する。
// 検査通過済みのWriteは、実呼出しが失効後へずれても取消しを保証しない。
// 物理切断もこの検査の保証ではない。
// authorityのlockをI/O中に保持しないため、停止は引き続き独立cutoffが担う。
type authorityWriter struct {
	authority *lease.Authority
	dst       io.Writer
}

func (w authorityWriter) Write(p []byte) (int, error) {
	if !w.authority.IsActive() {
		return 0, lease.ErrRejected
	}
	return w.dst.Write(p)
}

func (s *Session) forward(open ssh.NewChannel, reservation *lease.Reservation) {
	defer s.workers.Done()
	transferred := false
	defer func() {
		if !transferred {
			reservation.Release()
		}
	}()
	ctx, cancel := context.WithTimeout(s.authority.Context(), time.Second)
	defer cancel()
	target, cutoff, e := s.dial(ctx)
	if e != nil {
		open.Reject(ssh.ConnectionFailed, "接続を完了できません")
		return
	}
	if target == nil {
		open.Reject(ssh.ConnectionFailed, "接続がありません")
		return
	}
	s.mu.Lock()
	h := s.ownLocked(target)
	h.cutoff = cutoff
	h.reservation = reservation
	h.channelPending = true
	transferred = true
	if s.closing || !s.authority.IsActive() {
		s.closeOneLocked(h)
	}
	s.mu.Unlock()
	defer s.finishChannel(h)
	defer s.closeOne(h)
	if e = reservation.Promote(); e != nil {
		open.Reject(ssh.Prohibited, "失効しました")
		return
	}
	channel, requests, e := open.Accept()
	if e != nil {
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
		// TCP cleanup proceeds now; admission remains charged until the SSH
		// channel ends. Session revocation closes the owning transport too.
		s.closeOne(h)
		channel.Close()
		<-requestsDone
	}()
	if !s.authority.IsActive() {
		return
	}
	s.mu.Lock()
	s.forwarding++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.forwarding--; s.mu.Unlock() }()
	s.workers.Add(1)
	done := make(chan struct{})
	// 正常EOFは書込側だけ閉じ、反対方向の応答を待つ。
	// error/失効時は独立した所有ソケット停止経路で全停止する。
	go func() {
		defer s.workers.Done()
		defer close(done)
		_, err := io.Copy(authorityWriter{s.authority, target}, channel)
		if err == nil {
			if writer, ok := target.(interface{ CloseWrite() error }); ok {
				err = writer.CloseWrite()
			} else {
				err = errors.New("転送先が半閉鎖に未対応です")
			}
		}
		if err != nil {
			s.closeOne(h)
			channel.Close()
		}
	}()
	_, copyErr := io.Copy(authorityWriter{s.authority, channel}, target)
	if copyErr == nil {
		copyErr = channel.CloseWrite()
	}
	if copyErr != nil {
		s.closeOne(h)
		channel.Close()
	}

	<-done
}

// Close は同じローカル所有者の停止操作であり、保護された IPC の代わりではない。
// 戻らない Close や dial があれば期限付きで失敗し、REVOKING と参照を保持する。
func (s *Session) Close(ctx context.Context) error {
	s.authority.Revoke(s.authority.Identity())
	s.closeOwned()
	for {
		done := s.workers.Signal()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.authority.Done():
		}
		s.mu.Lock()
		if s.workers.Count() != 0 {
			s.mu.Unlock()
			continue
		}
		if len(s.owners) != 0 {
			s.mu.Unlock()
			return ErrUnconfirmed
		}
		if s.authority.Snapshot().State == lease.Terminated {
			s.mu.Unlock()
			return nil
		}
		err := s.authority.Terminate()
		s.mu.Unlock()
		return err
	}
}
