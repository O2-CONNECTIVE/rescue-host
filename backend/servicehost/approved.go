package servicehost

import (
	"context"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// Budgets は保護所有者が明示する予算。UI/SCM引数/envからは受け取らない。
// Observeは実行中の壁時計/失効観測間隔で、OS停止中の実行保証ではない。
type Budgets struct{ Startup, Cleanup, Observe time.Duration }

// Process は保護serviceが単独所有するprocess/JobのOS seam。
// Wait成功だけでGo Closeの確認やSSH停止の受入成功を意味しない。
// context期限に協調し、未確認時に所有handleを保持する必要がある。
type Process interface {
	Wait(context.Context) error
	Close(context.Context) error
}

// Launcherは起動予算内に準備し、部分起動の失敗でもProcessを返して所有を失わない。
// Startのcontextはstartup専用で、return後のprocess寿命を更新する権限はない。
type Launcher interface {
	Start(context.Context, *supervisor.Authority) (Process, error)
}

// WithRuntimeは将来の保護された承認入口専用。入口認証の実装ではない。
// default New/SCM/mainからはruntimeを設定せず、製品gateを閉じたままにする。
func WithRuntime(source ProtectedProvisioning, launcher Launcher, budgets Budgets) (*Host, error) {
	if source == nil || launcher == nil || budgets.Startup <= 0 || budgets.Cleanup <= 0 || budgets.Observe <= 0 {
		return nil, ErrProvisioningRejected
	}
	h := New(source)
	h.launcher, h.budgets = launcher, budgets
	return h, nil
}

// Closeは失効/取消の後にRun完了を待ち、保持した同じprocessだけを回収する。
// 予算切れは失敗として参照を保持する。再試行で新しい承認を作らない。
func (h *Host) Close(ctx context.Context) error {
	h.mu.Lock()
	h.used, h.closing = true, true
	started, done := h.started, h.done
	if h.authority != nil {
		h.authority.Revoke(h.authority.Snapshot(time.Now()).Grant.Binding)
	}
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Unlock()
	// 予算がなくても論理失効を先に確定し、待機/物理回収だけを未確認とする。
	if ctx == nil || ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	if _, finite := ctx.Deadline(); !finite {
		return ErrRuntimeUnconfirmed
	}
	if started {
		select {
		case <-done:
		case <-ctx.Done():
			return ErrRuntimeUnconfirmed
		}
	}
	return h.closeOwned(ctx)
}

func (h *Host) closeOwned(ctx context.Context) error {
	select {
	case <-h.cleanupGate:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	defer func() { h.cleanupGate <- struct{}{} }()
	if ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	h.mu.Lock()
	p := h.process
	h.mu.Unlock()
	if p != nil {
		if err := p.Close(ctx); err != nil || ctx.Err() != nil {
			return ErrRuntimeUnconfirmed
		}
	}
	return nil
}
