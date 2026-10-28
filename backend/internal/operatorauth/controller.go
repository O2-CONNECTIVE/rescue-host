package operatorauth

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"sync"
	"time"
)

// serverPeerは固定SCM/process/image/pipe creatorを保持照合する将来のnative
// producer専用。wire SID/PIDだけの公開constructorは提供しない。
type serverPeer interface {
	Recheck(context.Context) error
	Close(context.Context) error
}
type controllerOps struct {
	connect func(context.Context) (serverPeer, error)
	write   func(context.Context, []byte) error
	read    func(context.Context) ([]byte, error)
}
type Controller struct {
	mu                          sync.Mutex
	used, busy, closed, closing bool
	ops                         controllerOps
	peer                        serverPeer
	cleanup                     time.Duration
}

// NewNativeControllerはoperator用server認証profileが未設定の間は常に拒否。
// bootstrapauth.VerifyService(自主体検査)をoperator認証へ転用しない。
func NewNativeController() (*Controller, error) { return nil, ErrUnconfigured }

// Exchangeは一つの要求scopeだけを扱う。返るbytesは認証済み相手の応答でも、
// ownerのgrant/停止receiptを発行するものではない。channel全体の会話は未配線。
func (c *Controller) Exchange(ctx context.Context, frame []byte) (response []byte, err error) {
	if c == nil || !finite(ctx) {
		return nil, ErrDenied
	}
	c.mu.Lock()
	if c.used || c.closed || c.cleanup <= 0 || c.ops.connect == nil || c.ops.write == nil || c.ops.read == nil {
		c.mu.Unlock()
		return nil, ErrDenied
	}
	c.used, c.busy = true, true
	c.mu.Unlock()
	defer func() {
		if recover() != nil {
			response = nil
			err = ErrDenied
		}
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), c.cleanup)
		defer cancel()
		if c.Close(cleanup) != nil {
			response = nil
			err = ErrUnconfirmed
		} else if !finite(ctx) {
			response = nil
			err = ErrDenied
		}
	}()
	frame = append([]byte(nil), frame...)
	request, e := servicecontrol.DecodeUntrustedFrame(frame)
	if e != nil {
		return nil, ErrDenied
	}
	peer, e := c.ops.connect(ctx)
	c.mu.Lock()
	c.peer = peer
	c.mu.Unlock()
	if e != nil || peer == nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	if peer.Recheck(ctx) != nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	if c.ops.write(ctx, frame) != nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	if peer.Recheck(ctx) != nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	raw, e := c.ops.read(ctx)
	if e != nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	raw = append([]byte(nil), raw...)
	if peer.Recheck(ctx) != nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	reply, e := servicecontrol.DecodeUntrustedResponseFrame(raw)
	if e != nil || reply.RequestID != request.RequestID || reply.Sequence != request.Sequence {
		return nil, ErrDenied
	}
	if request.Type == servicecontrol.Hello {
		if reply.Type != "hello" || reply.ClientNonce != request.ClientNonce {
			return nil, ErrDenied
		}
	} else if reply.Type != "response" || reply.Command != request.Command || reply.BootEpoch != request.BootEpoch || reply.ChannelNonce != request.ChannelNonce {
		return nil, ErrDenied
	}
	if !c.live(ctx) {
		return nil, ErrDenied
	}
	return raw, nil
}
func (c *Controller) live(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && finite(ctx)
}
func (c *Controller) Close(ctx context.Context) (err error) {
	if c == nil {
		return ErrUnconfigured
	}
	c.mu.Lock()
	c.closed = true
	if c.busy || c.closing || !finite(ctx) {
		c.mu.Unlock()
		return ErrUnconfirmed
	}
	peer := c.peer
	if peer == nil {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	c.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = ErrUnconfirmed
		}
		c.mu.Lock()
		c.closing = false
		if err == nil {
			c.peer = nil
		}
		c.mu.Unlock()
	}()
	if peer.Close(ctx) != nil {
		return ErrUnconfirmed
	}
	return nil
}
