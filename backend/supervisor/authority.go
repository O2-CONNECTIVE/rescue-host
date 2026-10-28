// Package supervisor は将来の保護サービス向けの純粋な grant 判定を扱う。
// サービス登録、IPC、timer、通信、物理終了の確認は提供しない。
// hard ceiling はここが持ち、Go Session はそれを超えない短 lease と実通信を持つ。
// Windows service identity/token・IPC 認証・承認経路は未決であり、配線しない。
// 保護 caller は時刻を供給し、IPC 喪失等で Revoke し、独立に期限執行と回収を行う。
// 呼出しがなければ状態は進まない。親/監督 hang・OS sleep 中の執行は保証しない。
// この型は製品 IPC schema ではなく、製品 gate や既存受入判定も変更しない。
package supervisor

import (
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease"
)

var ErrDenied = errors.New("supervisor grant または期限条件が一致しません")

// Identity は Go Session と同じ同一性を使う。別の wire 定義ではない。
type Identity = lease.Identity

type Binding struct {
	// 保護 provisioning が未使用値を発行し、起動を跨いだ非再利用を担う。
	// この core は値を生成・increment・wrap しない。
	Generation uint64
	Identity   Identity
}

// GuestBoundary は保護 provisioning による表明。実 OS 検証の代わりにはならない。
type GuestBoundary uint8

const SeparatedNonAdminGuest GuestBoundary = 1

// ProvisionedGrant は保護 provisioning が検証・承認した値だけを渡す入力。
// UI、wire、保存 snapshot をこの型へ直接変換してはならない。
// GuestBoundary と KeySetDigest は OS 分離や実鍵集合の証明ではない。
type ProvisionedGrant struct {
	Binding             Binding
	KeySetDigest        [32]byte
	GuestBoundary       GuestBoundary
	HardDeadline        time.Time
	BackendLeaseCeiling time.Duration
}

type State string

const (
	Granted State = "granted"
	Revoked State = "revoked"
)

type Reason string

const (
	HardDeadlineReached Reason = "hard-deadline"
	ClockUnavailable    Reason = "clock-unavailable"
	ClockRegressed      Reason = "clock-regression"
	Uninitialized       Reason = "uninitialized"
	ExplicitStop        Reason = "explicit-stop"
)

type Snapshot struct {
	Grant  ProvisionedGrant
	State  State
	Reason Reason
}

// Authority は1承認につき1つ保持する。初回利用後にコピーしてはならない。
type Authority struct {
	mu     sync.Mutex
	grant  ProvisionedGrant
	state  State
	reason Reason
	last   time.Time
}

// New は新規承認にだけ使う。再起動時に旧 grant を再生成する入口ではない。
// 世代の非再利用・失効記録・新規承認の認証は保護 provisioning の必須依存。
// この core だけでは再起動を跨いだ replay 防止を保証しない。
func New(grant ProvisionedGrant, now time.Time) (*Authority, error) {
	now = now.Round(0)
	grant.HardDeadline = grant.HardDeadline.Round(0)
	if grant.Binding.Generation == 0 || !grant.Binding.Identity.Valid() || grant.KeySetDigest == ([32]byte{}) ||
		grant.GuestBoundary != SeparatedNonAdminGuest || now.IsZero() || grant.HardDeadline.IsZero() ||
		grant.BackendLeaseCeiling <= 0 || !now.Add(grant.BackendLeaseCeiling).Before(grant.HardDeadline) {
		return nil, ErrDenied
	}
	return &Authority{grant: grant, state: Granted, last: now}, nil
}

func (a *Authority) observe(now time.Time) {
	// 壁時計の巻戻りも観測するため、埋め込まれた monotonic 値には依存しない。
	// now は未信頼入力ではなく、保護 caller が順序を保証した時刻値に限定する。
	now = now.Round(0)
	if a.state == "" {
		a.state, a.reason = Revoked, Uninitialized
	}
	if a.state == Granted {
		switch {
		case now.IsZero():
			a.state, a.reason = Revoked, ClockUnavailable
		case now.Before(a.last):
			a.state, a.reason = Revoked, ClockRegressed
		case !now.Before(a.grant.HardDeadline):
			a.state, a.reason = Revoked, HardDeadlineReached
		}
		a.last = now
	}
}

// Snapshot は期限を再判定するが、新規認可・lease 更新・期限延長はしない。
func (a *Authority) Snapshot(now time.Time) Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observe(now)
	return Snapshot{Grant: a.grant, State: a.state, Reason: a.reason}
}

// Revoke は同じ grant だけを不可逆に失効させる。通信終了の確認ではない。
func (a *Authority) Revoke(binding Binding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.grant.Binding.Generation == 0 || binding != a.grant.Binding {
		return ErrDenied
	}
	if a.state == Granted {
		a.state, a.reason = Revoked, ExplicitStop
	}
	return nil
}

// CheckBackendLease は保護 caller の提案期限が上限内かだけを判定する。
// 成功は更新者の認証・連番検証・更新承認ではなく、lease を発行/更新しない。
// UI の poll をこの結果で Session.Renew へ接続してはならない。
func (a *Authority) CheckBackendLease(binding Binding, deadline, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if binding != a.grant.Binding {
		return ErrDenied
	}
	a.observe(now)
	if a.state != Granted {
		return ErrDenied
	}
	now, deadline = now.Round(0), deadline.Round(0)
	if !deadline.After(now) || deadline.After(now.Add(a.grant.BackendLeaseCeiling)) || deadline.After(a.grant.HardDeadline) {
		return ErrDenied
	}
	return nil
}
