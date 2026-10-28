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
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

var (
	ErrProvisioningUnconfigured = errors.New("保護 provisioning が未設定です")
	ErrProvisioningRejected     = errors.New("保護 provisioning を受理できません")
	ErrActivationUnconfigured   = errors.New("backend activation の保護境界が未設定です")
	ErrAlreadyUsed              = errors.New("service host は使用済みです")
	ErrUnsupportedPlatform      = errors.New("Windows SCM を利用できない OS です")
	ErrSCMUnavailable           = errors.New("SCM dispatcher を利用できません")
	ErrRuntimeUnconfirmed       = errors.New("所有runtimeの終了を確認できません")
)

// ProtectedProvisioning は将来の保護された承認済み値の読取境界。
// 固定の保護場所・信頼元・token/ACL/非管理者 guest の検証は実装者の必須責務。
// UI、SCM 引数、環境変数、任意 path/URL、保存 snapshot を認可源にしてはならない。
// context cancellation に協調すること。現在、その実装は提供しない。
type ProtectedProvisioning interface {
	ReadApproved(context.Context) (ephemeral.ApprovedPolicy, error)
}

// Host は1起動の所有物で、初回利用後にコピーしてはならない。
// 初回承認はIdentityを要求せず、fresh issuerが単回のbindingを発行する。
// 起動を跨ぐ旧grant復元を提供せず、入口認証/OS保護を代用しない。
type Host struct {
	mu               sync.Mutex
	used             bool
	provisioning     ProtectedProvisioning
	borrowedIssuer   *ephemeral.Issuer
	serviceStop      func(error)
	authority        *supervisor.Authority
	launcher         Launcher
	budgets          Budgets
	process          Process
	cancel           context.CancelFunc
	started, closing bool
	done             chan struct{}
	cleanupGate      chan struct{}
}

func New(provisioning ProtectedProvisioning) *Host {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Host{provisioning: provisioning, done: make(chan struct{}), cleanupGate: gate}
}

// Run が bootstrapauth.ErrFatal / protectedkey.ErrFatal を返す場合、呼出し元は
// 同じ goroutine で作業を続けず終了し、疑わしい OS thread を退役させる。
func (h *Host) Run(ctx context.Context) (result error) {
	h.mu.Lock()
	if h.done == nil || h.cleanupGate == nil {
		h.mu.Unlock()
		return ErrProvisioningRejected
	}
	if h.used {
		h.mu.Unlock()
		return ErrAlreadyUsed
	}
	h.used = true
	h.started = true
	h.mu.Unlock()
	defer close(h.done)
	if ctx == nil {
		return ErrProvisioningRejected
	}
	ctx, cancelOwned := context.WithCancel(ctx)
	defer cancelOwned()
	h.mu.Lock()
	h.cancel = cancelOwned
	if h.closing {
		cancelOwned()
	}
	h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.provisioning == nil {
		return ErrProvisioningUnconfigured
	}
	policy, err := h.provisioning.ReadApproved(ctx)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return ErrProvisioningRejected
	}
	issuer := h.borrowedIssuer
	if issuer == nil {
		issuer, err = ephemeral.New()
		if err != nil {
			return ErrProvisioningRejected
		}
		defer issuer.Close()
	}
	challenge, err := issuer.Prepare(policy)
	if err != nil {
		return ErrProvisioningRejected
	}
	releaseGrant := func() {
		if h.borrowedIssuer == nil {
			_ = issuer.Close()
		} else {
			_ = issuer.Revoke(challenge.Binding())
		}
	}
	defer releaseGrant()
	issued := IssuedLaunch{instance: issuer.Snapshot().InstanceEpoch, binding: challenge.Binding()}
	runCtx, cancel := context.WithDeadline(ctx, policy.HardDeadline)
	defer cancel()
	var owned Process
	defer func() {
		// service全体の発行拒否/全失効も、部分process回収より先に確定する。
		if h.serviceStop != nil {
			h.serviceStop(result)
		}
		// 認可失効をprocess回収より先に確定する。取消済のctxをcleanupへ流用しない。
		releaseGrant()
		cancel()
		if owned != nil {
			cleanup, end := context.WithTimeout(context.Background(), h.budgets.Cleanup)
			defer end()
			if err := h.closeOwned(cleanup); err != nil || cleanup.Err() != nil {
				result = runtimeFailure(result, err)
			}
		}
	}()
	var launchErr error
	a, err := issuer.Launch(runCtx, challenge, time.Now, func(_ context.Context, authority *supervisor.Authority) error {
		h.mu.Lock()
		h.authority = authority
		h.mu.Unlock()
		if h.launcher == nil {
			launchErr = ErrActivationUnconfigured
			return launchErr
		}
		if h.budgets.Observe > policy.BackendLeaseCeiling {
			launchErr = ErrProvisioningRejected
			return launchErr
		}
		startup, end := context.WithTimeout(runCtx, h.budgets.Startup)
		defer end()
		if target, ok := h.launcher.(IssuedLauncher); ok {
			owned, launchErr = target.StartIssued(startup, authority, issued)
		} else {
			owned, launchErr = h.launcher.Start(startup, authority)
		}
		h.mu.Lock()
		h.process = owned
		h.mu.Unlock()
		if startup.Err() != nil || owned == nil {
			launchErr = runtimeFailure(launchErr)
		}
		return launchErr
	})
	if err != nil {
		if launchErr == ErrActivationUnconfigured {
			return launchErr
		}
		h.mu.Lock()
		hasAuthority := h.authority != nil
		h.mu.Unlock()
		if a == nil && !hasAuthority {
			return ErrProvisioningRejected
		}
		return runtimeFailure(launchErr)
	}
	// Go側のleaseとは別に、serviceが元のhard deadlineとclock regressionを観測する。
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(h.budgets.Observe)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if a.Snapshot(time.Now()).State != supervisor.Granted {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-watchDone }()
	waitErr := owned.Wait(runCtx)
	if runCtx.Err() != nil {
		return runCtx.Err()
	}
	// exit0/Job空だけではGo Closeの終了確認を得られない。予期しない退出は閉じる。
	_ = waitErr
	return ErrRuntimeUnconfirmed
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

// Revokeは同じbindingを直ちに失効させ、所有runtimeの待機を取消す。復活/renewはない。
func (h *Host) Revoke(binding supervisor.Binding) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.authority == nil || h.authority.Revoke(binding) != nil {
		return supervisor.ErrDenied
	}
	if h.cancel != nil {
		h.cancel()
	}
	return nil
}
