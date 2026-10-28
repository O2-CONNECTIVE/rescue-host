package localcontrol

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"sync"
	"time"
)

// peerToken はkernel adapterだけが取得する。wireのSID文字列は受け付けない。
type peerToken struct {
	sid              string
	tokenType, level uint32
}
type kernel interface {
	create(PipePlan) (uintptr, error)
	close(uintptr) error
	inspect(uintptr) error
	connect(context.Context, uintptr) error
	read(context.Context, uintptr) ([]byte, error)
	write(context.Context, uintptr, []byte) error
	serverSID() (string, error)
	pin()
	unpin()
	hasThreadToken() (bool, error)
	impersonate(uintptr) error
	clientToken() (peerToken, error)
	revert() error
	fatal()
}

type Boundary struct {
	policy   *Policy
	api      kernel
	handler  stdio.Service
	used     bool
	openGate chan struct{}
}
type Endpoint struct {
	boundary         *Boundary
	handle           uintptr
	gate             chan struct{}
	closed, poisoned bool
	connected        bool
	activeMu         sync.Mutex
	activeCancel     context.CancelFunc
	stopping         bool
	cleanupErr       error
}

func newBoundary(config Config, api kernel, handler stdio.Service) (*Boundary, error) {
	policy, err := NewPolicy(config)
	if err != nil {
		return nil, err
	}
	if api == nil || handler == nil {
		return nil, ErrConfig
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Boundary{policy: policy, api: api, handler: handler, openGate: gate}, nil
}

// Open は製品のruntime接続前には常に拒否する。config/envの解除flagはない。
func (b *Boundary) Open(context.Context) (*Endpoint, error) { return nil, ErrClosed }

// openはnative factoryの境界。現在はin-memory API試験だけが呼び出す。
func (b *Boundary) open(ctx context.Context) (*Endpoint, error) {
	if err := take(ctx, b.openGate); err != nil {
		return nil, err
	}
	defer func() { b.openGate <- struct{}{} }()
	if b.used {
		return nil, ErrDenied
	}
	b.used = true
	sid, err := b.api.serverSID()
	if err != nil || sid != b.policy.config.ServiceSID {
		return nil, ErrDenied
	}
	b.api.pin()
	// object作成にもthreadの実効contextが作用するため、process SIDだけで作らない。
	exists, probeErr := b.api.hasThreadToken()
	if probeErr != nil || exists {
		b.api.fatal()
		return nil, ErrFatal
	}
	if ctx.Err() != nil {
		b.api.unpin()
		return nil, ErrDenied
	}
	handle, err := b.api.create(b.policy.plan)
	b.api.unpin()
	if err != nil || handle == 0 || handle == ^uintptr(0) {
		return nil, ErrDenied
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Endpoint{boundary: b, handle: handle, gate: gate}, nil
}

func (e *Endpoint) acquire(ctx context.Context) error {
	if e == nil {
		return ErrDenied
	}
	return take(ctx, e.gate)
}

func take(ctx context.Context, gate chan struct{}) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrDenied
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrDenied
	}
	select {
	case <-ctx.Done():
		return ErrDenied
	case <-gate:
	}
	if ctx.Err() != nil {
		gate <- struct{}{}
		return ErrDenied
	}
	return nil
}

// authenticateLocked は最後にreadしたmessageのkernel tokenだけを認可する。
// 呼出側はreadからresponse完了まで同じgateを保持する。
func (e *Endpoint) authenticateLocked(ctx context.Context) error {
	api, config := e.boundary.api, e.boundary.policy.config
	if err := api.inspect(e.handle); err != nil {
		return ErrDenied
	}
	sid, err := api.serverSID()
	if err != nil || sid != config.ServiceSID {
		return ErrDenied
	}
	api.pin()
	// Process TokenUserの一致だけでthreadがcleanとは推定しない。
	exists, err := api.hasThreadToken()
	if err != nil || exists {
		e.poisoned = true
		api.fatal()
		return ErrFatal
	}
	if err := api.impersonate(e.handle); err != nil {
		api.unpin()
		return ErrDenied
	}
	peer, tokenErr := api.clientToken()
	if err := api.revert(); err != nil {
		e.poisoned = true
		api.fatal()
		return ErrFatal
	}
	api.unpin()
	if tokenErr == ErrFatal {
		e.poisoned = true
		api.fatal()
		return ErrFatal
	}
	if tokenErr != nil || peer.tokenType != 2 || peer.level < 1 || peer.level > 3 || peer.sid == config.GuestSID || (peer.sid != config.OperatorSID && peer.sid != config.ServiceSID) || ctx.Err() != nil {
		return ErrDenied
	}
	return nil
}

func (e *Endpoint) Close(ctx context.Context) error {
	if e == nil {
		return ErrDenied
	}
	e.stopOperations() // pending accept/read/writeを取消し、token scopeへcloseを割り込ませない。
	if err := e.acquire(ctx); err != nil {
		return err
	}
	defer func() { e.gate <- struct{}{} }()
	if e.poisoned {
		return ErrFatal
	}
	if e.closed {
		return e.cleanupErr
	}
	return e.closeLocked()
}

func (e *Endpoint) closeLocked() error {
	if e.closed {
		return e.cleanupErr
	}
	e.stopOperations()
	if err := e.boundary.api.close(e.handle); err != nil {
		e.poisoned = true
		e.boundary.api.fatal()
		return ErrFatal
	}
	e.closed = true
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.boundary.handler.Close(cleanup); err != nil || cleanup.Err() != nil {
		e.cleanupErr = ErrUnconfirmed
	}
	return e.cleanupErr
}
