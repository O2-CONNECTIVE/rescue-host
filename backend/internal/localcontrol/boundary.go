package localcontrol

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
)

type Command struct {
	Generation uint64
	Identity   backend.Identity
	Operation  control.Operation
}

// peerToken はkernel adapterだけが取得する。wireのSID文字列は受け付けない。
type peerToken struct {
	sid              string
	tokenType, level uint32
}
type kernel interface {
	create(PipePlan) (uintptr, error)
	close(uintptr) error
	inspect(uintptr) error
	read(context.Context, uintptr) (Command, error)
	serverSID() (string, error)
	pin()
	unpin()
	hasThreadToken() (bool, error)
	impersonate(uintptr) error
	clientToken() (peerToken, error)
	revert() error
	fatal()
}
type dispatcher interface {
	Dispatch(context.Context, control.Operation, backend.Identity) (backend.Snapshot, error)
}

type Boundary struct {
	policy     *Policy
	api        kernel
	dispatcher dispatcher
	used       bool
	openGate   chan struct{}
}
type Endpoint struct {
	boundary         *Boundary
	handle           uintptr
	gate             chan struct{}
	closed, poisoned bool
}

func newBoundary(config Config, api kernel, dispatch dispatcher) (*Boundary, error) {
	policy, err := NewPolicy(config)
	if err != nil {
		return nil, err
	}
	if api == nil || dispatch == nil {
		return nil, ErrConfig
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Boundary{policy: policy, api: api, dispatcher: dispatch, openGate: gate}, nil
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

// Nextは所有handleのreadから認証/revert/dispatchまでをCloseと直列化する。
// native read/acceptは未配線なので、現在のruntimeではErrClosedにする。
func (e *Endpoint) Next(ctx context.Context) (backend.Snapshot, error) {
	if err := e.acquire(ctx); err != nil {
		return backend.Snapshot{}, err
	}
	defer func() { e.gate <- struct{}{} }()
	if e.poisoned {
		return backend.Snapshot{}, ErrFatal
	}
	if e.closed {
		return backend.Snapshot{}, ErrClosed
	}
	command, err := e.boundary.api.read(ctx, e.handle)
	if err != nil {
		if err == ErrClosed {
			return backend.Snapshot{}, ErrClosed
		}
		return backend.Snapshot{}, ErrDenied
	}
	return e.dispatchLocked(ctx, command)
}

func (e *Endpoint) dispatchLocked(ctx context.Context, command Command) (backend.Snapshot, error) {
	if command.Generation != e.boundary.policy.config.Generation || command.Identity != e.boundary.policy.config.Identity || (command.Operation != control.ReadSnapshot && command.Operation != control.Stop) {
		return backend.Snapshot{}, ErrDenied
	}
	api, config := e.boundary.api, e.boundary.policy.config
	if err := api.inspect(e.handle); err != nil {
		return backend.Snapshot{}, ErrDenied
	}
	sid, err := api.serverSID()
	if err != nil || sid != config.ServiceSID {
		return backend.Snapshot{}, ErrDenied
	}
	api.pin()
	// Process TokenUserの一致だけでthreadがcleanとは推定しない。
	exists, err := api.hasThreadToken()
	if err != nil || exists {
		e.poisoned = true
		api.fatal()
		return backend.Snapshot{}, ErrFatal
	}
	if err := api.impersonate(e.handle); err != nil {
		api.unpin()
		return backend.Snapshot{}, ErrDenied
	}
	peer, tokenErr := api.clientToken()
	if err := api.revert(); err != nil {
		e.poisoned = true
		api.fatal()
		return backend.Snapshot{}, ErrFatal
	}
	api.unpin()
	if tokenErr == ErrFatal {
		e.poisoned = true
		api.fatal()
		return backend.Snapshot{}, ErrFatal
	}
	if tokenErr != nil || peer.tokenType != 2 || peer.level < 1 || peer.level > 3 || peer.sid == config.GuestSID || (peer.sid != config.OperatorSID && peer.sid != config.ServiceSID) || ctx.Err() != nil {
		return backend.Snapshot{}, ErrDenied
	}
	snapshot, err := e.boundary.dispatcher.Dispatch(ctx, command.Operation, command.Identity)
	if err != nil {
		return backend.Snapshot{}, ErrUnconfirmed
	}
	return snapshot, nil
}

func (e *Endpoint) Close(ctx context.Context) error {
	if err := e.acquire(ctx); err != nil {
		return err
	}
	defer func() { e.gate <- struct{}{} }()
	if e.poisoned {
		return ErrFatal
	}
	if e.closed {
		return nil
	}
	if err := e.boundary.api.close(e.handle); err != nil {
		e.poisoned = true
		e.boundary.api.fatal()
		return ErrFatal
	}
	e.closed = true
	return nil
}
