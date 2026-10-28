// Package servicehost は未配線の Windows service host の準備境界である。
// SCM の状態は製品 IPC の Go state や SSH 接続許可ではない。
// 実 token、service identity/privilege、ACL、IPC 承認経路は未決。
// LocalService と per-service SID は候補で、LocalSystem fallback は提供しない。
// 保護 provisioning と backend activation は未設定時に閉じる。
package servicehost

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

var (
	ErrProvisioningUnconfigured = errors.New("保護 provisioning が未設定です")
	ErrProvisioningRejected     = errors.New("保護 provisioning を受理できません")
	ErrActivationUnconfigured   = errors.New("backend activation の保護境界が未設定です")
	ErrAlreadyUsed              = errors.New("service host は使用済みです")
	ErrUnsupportedPlatform      = errors.New("Windows SCM を利用できない OS です")
	ErrSCMUnavailable           = errors.New("SCM dispatcher を利用できません")
)

// ProtectedProvisioning は将来の保護された承認済み値の読取境界。
// 固定の保護場所・信頼元・token/ACL/非管理者 guest の検証は実装者の必須責務。
// UI、SCM 引数、環境変数、任意 path/URL、保存 snapshot を認可源にしてはならない。
// context cancellation に協調すること。現在、その実装は提供しない。
type ProtectedProvisioning interface {
	ReadApproved(context.Context) (supervisor.ProvisionedGrant, error)
}

// Host は1起動の所有物で、初回利用後にコピーしてはならない。
// 再起動時の世代再利用防止と失効記録は別の保護 provisioning の責務。
type Host struct {
	mu           sync.Mutex
	used         bool
	provisioning ProtectedProvisioning
	authority    *supervisor.Authority
}

func New(provisioning ProtectedProvisioning) *Host {
	return &Host{provisioning: provisioning}
}

func (h *Host) Run(ctx context.Context) error {
	h.mu.Lock()
	if h.used {
		h.mu.Unlock()
		return ErrAlreadyUsed
	}
	h.used = true
	h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.provisioning == nil {
		return ErrProvisioningUnconfigured
	}
	grant, err := h.provisioning.ReadApproved(ctx)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return ErrProvisioningRejected
	}
	authority, err := supervisor.New(grant, time.Now())
	if err != nil {
		return ErrProvisioningRejected
	}
	h.mu.Lock()
	h.authority = authority
	h.mu.Unlock()
	defer authority.Revoke(grant.Binding)
	// 実 token/ACL と承認済み IPC、hard cap の執行と Go 短 lease の連携は未配線。
	// 検証済み grant があっても Ready/Activate/Renew や接続公開へ進めない。
	return ErrActivationUnconfigured
}

// Snapshot は検証した grant の観測だけで、policy の再読取や更新をしない。
// granted は service/SSH ready ではなく、存在しない grant を成功へ変換しない。
func (h *Host) Snapshot() (supervisor.Snapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.authority == nil {
		return supervisor.Snapshot{}, false
	}
	return h.authority.Snapshot(time.Now()), true
}
