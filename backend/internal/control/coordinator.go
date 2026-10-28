// Package control は既存 Session への未配線の制御境界である。
// wire schema、IPC 認証、保護監督、Session の生成は提供しない。
package control

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
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
	session  session
	identity backend.Identity
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

// Start は保護監督・契約の接続前には常に拒否する。Ready/Activateは呼ばない。
func (c *Coordinator) Start(context.Context, backend.Identity) error { return ErrStartClosed }

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
	if err := c.session.Revoke(id); err != nil {
		return backend.Snapshot{}, ErrUnavailable
	}
	closeErr := c.session.Close(ctx)
	snapshot, err := c.Snapshot(id)
	if err != nil {
		return backend.Snapshot{}, err
	}
	if closeErr != nil || ctx.Err() != nil || snapshot.State != "terminated" {
		return snapshot, ErrUnconfirmed
	}
	return snapshot, nil
}
