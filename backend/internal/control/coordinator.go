// Package control は既存 Session への未配線の制御境界である。
// wire schema、IPC 認証、保護監督、Session の生成は提供しない。
package control

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

var (
	ErrUnavailable = errors.New("制御対象を確認できません")
	ErrIdentity    = errors.New("制御対象の同一性が一致しません")
	ErrBudget      = errors.New("終了確認の期限が必要です")
	ErrUnconfirmed = errors.New("Sessionの終了を確認できません")
	ErrStartClosed = errors.New("製品の開始ゲートは閉鎖されています")
)

type session interface {
	Snapshot() backend.Snapshot
	Revoke(backend.Identity) error
	Close(context.Context) error
}

type Coordinator struct {
	mu           sync.Mutex
	session      session
	identity     backend.Identity
	prepared     bool
	started      bool
	stopped      bool
	deadline     time.Time
	closeTimeout time.Duration
}

// Operation はGo内の呼出し種別であり、wireの版やfieldを定義しない。
type Operation uint8

const (
	Start Operation = iota + 1
	ReadSnapshot
	Stop
)

func (c *Coordinator) Dispatch(ctx context.Context, operation Operation, id backend.Identity) (backend.Snapshot, error) {
	switch operation {
	case Start:
		return backend.Snapshot{}, c.Start(ctx, id)
	case ReadSnapshot:
		return c.Snapshot(id)
	case Stop:
		return c.Stop(ctx, id)
	default:
		return backend.Snapshot{}, ErrUnavailable
	}
}

// New は呼出側が既に所有した1 Sessionを引き取る。新規Sessionは生成しない。
func New(s *backend.Session) (*Coordinator, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	return newCoordinator(s)
}

func newCoordinator(s session) (*Coordinator, error) {
	id := s.Snapshot().Identity
	if !id.Valid() {
		return nil, ErrIdentity
	}
	return &Coordinator{session: s, identity: id}, nil
}

// NewPrepared は保護された型付きbootstrapだけが呼ぶ準備入口。
// wireからgrantを作らず、supervisorの権威を新規作成・延長しない。
// SessionのConfig/鍵集合/短leaseとgrantの照合はruntimeが先に済ませる。
func NewPrepared(s *backend.Session, grant supervisor.ProvisionedGrant, closeTimeout time.Duration) (*Coordinator, error) {
	c, err := New(s)
	if err != nil {
		return nil, err
	}
	if grant.Binding.Generation == 0 || grant.Binding.Identity != c.identity || grant.GuestBoundary != supervisor.SeparatedNonAdminGuest || grant.KeySetDigest == ([32]byte{}) || grant.BackendLeaseCeiling <= 0 || !time.Now().Before(grant.HardDeadline) || s.Snapshot().State != "prepared" || closeTimeout <= 0 {
		return nil, ErrIdentity
	}
	c.prepared, c.deadline = true, grant.HardDeadline.Round(0)
	c.closeTimeout = closeTimeout
	return c, nil
}

// Start は既定constructorでは拒否する。保護bootstrapで準備した同じSessionだけを
// Ready→Activateへ進め、失敗/取消後の再試行や復活は許可しない。
func (c *Coordinator) Start(ctx context.Context, id backend.Identity) error {
	if c == nil {
		return ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.prepared {
		return ErrStartClosed
	}
	if ctx == nil {
		return ErrBudget
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrBudget
	}
	if id != c.identity {
		return ErrIdentity
	}
	if c.started || c.stopped {
		return ErrUnavailable
	}
	c.started = true
	active, ok := c.session.(interface {
		Ready(backend.Identity) error
		Activate(backend.Identity) error
	})
	if !ok || !time.Now().Before(c.deadline) || ctx.Err() != nil || active.Ready(id) != nil || !time.Now().Before(c.deadline) || ctx.Err() != nil || active.Activate(id) != nil || !time.Now().Before(c.deadline) || ctx.Err() != nil {
		_ = c.session.Revoke(id)
		return ErrUnavailable
	}
	return nil
}

// Snapshot はGoの観測値だけを返し、認可・lease更新・期限延長を行わない。
func (c *Coordinator) Snapshot(id backend.Identity) (backend.Snapshot, error) {
	if c == nil || c.session == nil {
		return backend.Snapshot{}, ErrUnavailable
	}
	if id != c.identity {
		return backend.Snapshot{}, ErrIdentity
	}
	snapshot := c.session.Snapshot()
	if snapshot.Identity != c.identity {
		return backend.Snapshot{}, ErrIdentity
	}
	return snapshot, nil
}

func (c *Coordinator) Stop(ctx context.Context, id backend.Identity) (backend.Snapshot, error) {
	if ctx == nil {
		return backend.Snapshot{}, ErrBudget
	}
	if _, ok := ctx.Deadline(); !ok {
		return backend.Snapshot{}, ErrBudget
	}
	if err := ctx.Err(); err != nil {
		return backend.Snapshot{}, err
	}
	if _, err := c.Snapshot(id); err != nil {
		return backend.Snapshot{}, err
	}
	c.mu.Lock()
	c.stopped = true
	err := c.session.Revoke(id)
	c.mu.Unlock()
	if err != nil {
		return backend.Snapshot{}, ErrUnavailable
	}
	closeCtx := ctx
	if c.closeTimeout > 0 {
		var cancel context.CancelFunc
		closeCtx, cancel = context.WithTimeout(ctx, c.closeTimeout)
		defer cancel()
	}
	closeErr := c.session.Close(closeCtx)
	snapshot, err := c.Snapshot(id)
	if err != nil {
		return backend.Snapshot{}, err
	}
	if closeErr != nil || closeCtx.Err() != nil || snapshot.State != "terminated" {
		return snapshot, ErrUnconfirmed
	}
	return snapshot, nil
}
