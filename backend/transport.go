package backend

import (
	"context"
	"net"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease"
)

// AcceptedSourceは保護側が供給する受理済み接続の所有境界。認証・承認ではない。
// source/endpointをwireや環境変数から選択せず、未設定時は所有しない。
// Acceptは取消、Closeは明示予算に協調する。任意の戻らないOS adapterは強制できない。
// Closeのnilは実sourceの終了確認を意味し、単なる取消要求で代用しない。
type AcceptedSource interface {
	Accept(context.Context) (net.Conn, error)
	Close(context.Context) error
}
type acceptedTransportOwner struct {
	source          AcceptedSource
	ctx             context.Context
	cancel          context.CancelFunc
	cleanup         time.Duration
	closing, closed bool
	pending         map[*acceptedTransport]struct{}
	changed         chan struct{}
}

// AttachAcceptedSourceはprepared中の同じSessionに一度だけsourceを移す。
// errorなら所有は移らずcallerに残る。listener生成・認証・Activateは行わない。
func (s *Session) AttachAcceptedSource(source AcceptedSource, hardDeadline time.Time, cleanupTimeout time.Duration) error {
	if s == nil || s.authority == nil || source == nil || cleanupTimeout <= 0 || hardDeadline.IsZero() || !time.Now().Before(hardDeadline) {
		return ErrInvalidConfig
	}
	ctx, cancel := context.WithDeadline(s.authority.Context(), hardDeadline)
	keep := false
	defer func() {
		if !keep {
			cancel()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.accepted != nil || s.authority.Snapshot().State != lease.Prepared {
		return ErrNotAccepted
	}
	s.attachSourceLocked(source, ctx, cancel, cleanupTimeout)
	keep = true
	return nil
}

// attachSourceLockedは検査済みの移転だけを行い、失敗しない。
func (s *Session) attachSourceLocked(source AcceptedSource, ctx context.Context, cancel context.CancelFunc, cleanupTimeout time.Duration) {
	owner := &acceptedTransportOwner{source: source, ctx: ctx, cancel: cancel, cleanup: cleanupTimeout, pending: make(map[*acceptedTransport]struct{}), changed: make(chan struct{})}
	s.accepted = owner
	s.workers.Add(1)
	close(s.acceptedAttached)
	go owner.accept(s)
}

type acceptedTransport struct {
	conn                            net.Conn
	accepting, dispatching, closing bool
	transferred                     bool
}

func (o *acceptedTransportOwner) notifyLocked() { close(o.changed); o.changed = make(chan struct{}) }
func (o *acceptedTransportOwner) accept(s *Session) {
	defer s.workers.Done()
	select {
	case <-s.activated:
	case <-o.ctx.Done():
		_ = s.Revoke(s.authority.Identity())
		return
	}
	for {
		s.mu.Lock()
		if s.closing || o.ctx.Err() != nil || !s.authority.IsActive() {
			s.mu.Unlock()
			_ = s.Revoke(s.authority.Identity())
			return
		}
		if len(o.pending)+s.transports >= s.maxTransports {
			changed := o.changed
			s.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-o.ctx.Done():
				_ = s.Revoke(s.authority.Identity())
				return
			}
		}
		entry := &acceptedTransport{accepting: true}
		o.pending[entry] = struct{}{}
		s.mu.Unlock()
		conn, err := callAcceptedSource(o.source, o.ctx)
		s.mu.Lock()
		entry.conn, entry.accepting = conn, false
		if err != nil || conn == nil || s.closing || o.ctx.Err() != nil || !s.authority.IsActive() {
			if conn == nil {
				delete(o.pending, entry)
				o.notifyLocked()
			} else {
				o.closeRejectedLocked(s, entry)
			}
			s.mu.Unlock()
			_ = s.Revoke(s.authority.Identity())
			return
		}
		entry.dispatching = true
		s.workers.Add(1)
		s.mu.Unlock()
		go o.deliver(s, entry)
	}
}
func (o *acceptedTransportOwner) deliver(s *Session, entry *acceptedTransport) {
	defer s.workers.Done()
	_ = s.serveAccepted(o, entry)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.dispatching = false
	if !entry.transferred {
		o.closeRejectedLocked(s, entry)
	}
	// 実ownLockedと同じcritical sectionで移転済みなら、SessionだけがCloseする。
}
func (o *acceptedTransportOwner) closeRejectedLocked(s *Session, entry *acceptedTransport) {
	if entry.accepting || entry.dispatching || entry.closing || entry.conn == nil {
		return
	}
	entry.closing = true
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		err := closeAcceptedConnection(entry.conn)
		if err != nil {
			_ = s.Revoke(s.authority.Identity())
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		entry.closing = false
		if err == nil {
			delete(o.pending, entry)
			o.notifyLocked()
		}
	}()
}
func callAcceptedSource(source AcceptedSource, ctx context.Context) (conn net.Conn, err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnconfirmed
		}
	}()
	return source.Accept(ctx)
}
func closeAcceptedConnection(conn net.Conn) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnconfirmed
		}
	}()
	return conn.Close()
}

// Session.mu下では登録だけを行い、外部sourceのI/Oはworkerで実行する。
func (o *acceptedTransportOwner) stopLocked(s *Session) {
	for entry := range o.pending {
		o.closeRejectedLocked(s, entry)
	}
	if o.closing || o.closed {
		return
	}
	o.closing = true
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		o.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), o.cleanup)
		err := closeAcceptedSource(o.source, ctx)
		expired := ctx.Err() != nil
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		o.closing = false
		o.closed = err == nil && !expired
	}()
}

func closeAcceptedSource(source AcceptedSource, ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnconfirmed
		}
	}()
	return source.Close(ctx)
}

// 既存のSession lifetime watcherがsource期限も監督する。Accept自身のreturnを
// 期限執行の前提にせず、別のauthorityやtimer更新を作らない。
func (s *Session) watchAcceptedAndAuthority() {
	defer s.workers.Done()
	select {
	case <-s.authority.Context().Done():
	case <-s.acceptedAttached:
		s.mu.Lock()
		owner := s.accepted
		s.mu.Unlock()
		select {
		case <-s.authority.Context().Done():
		case <-owner.ctx.Done():
			_ = s.Revoke(s.authority.Identity())
		}
	}
	s.closeOwned()
}
