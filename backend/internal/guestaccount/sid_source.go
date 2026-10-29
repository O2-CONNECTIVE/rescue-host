package guestaccount

import (
	"context"
	"sync"
	"sync/atomic"
)

type sidExecutor interface {
	execute(context.Context, []byte) ([]byte, error)
	Close(context.Context) error
}

// sidSourceは既に所有元が確定した一つのSIDだけを清掃する。create元/完全権限
// producerを作らず、元inspectorの拒否を成功へ変換しない。公開factoryは閉鎖。
type sidSource struct {
	self                       *sidSource
	mu                         sync.Mutex
	ownerMu                    sync.Mutex
	stop                       atomic.Bool
	plan                       Plan
	binding                    binding
	seal                       *seal
	inspector                  accountInspector
	issue                      func() (sidExecutor, error)
	owners                     []sidExecutor
	disabled, deleted, unknown bool
	verified                   accountSnapshot
}

func newSIDSource(p Plan, b binding, s *seal, inspector accountInspector, issue func() (sidExecutor, error)) (*sidSource, error) {
	if !p.validBinding(b) || s == nil || inspector == nil || issue == nil {
		return nil, ErrUnconfigured
	}
	a := &sidSource{plan: p, binding: b, seal: s, inspector: inspector, issue: issue}
	a.self = a
	return a, nil
}
func (a *sidSource) enter(ctx context.Context, b binding) bool {
	if a == nil || a.self != a || a.stop.Load() || !finite(ctx) || b != a.binding || !a.plan.validBinding(b) || !a.mu.TryLock() {
		return false
	}
	if a.stop.Load() {
		a.mu.Unlock()
		return false
	}
	return true
}
func (a *sidSource) createDisabled(context.Context, binding) (accountReceipt, error) {
	return accountReceipt{}, ErrUnconfigured
}
func (a *sidSource) inspect(ctx context.Context, b binding) (r accountSnapshot, err error) {
	if !a.enter(ctx, b) {
		return r, ErrDenied
	}
	defer a.mu.Unlock()
	if a.unknown {
		return r, ErrUnknown
	}
	if a.deleted {
		// 正確なSIDの実削除成功後だけの同lifetime tombstone。restart復元、
		// not-found、同名照会、未知の削除結果から不在を推測しない。
		return accountSnapshot{seal: a.seal, binding: b, presence: absent, complete: true}, nil
	}
	a.verified = accountSnapshot{}
	r, err = a.inspector.inspect(ctx, b)
	if err == nil && finite(ctx) && r.seal == a.seal && r.binding == b && r.presence == exact && r.complete && r.disabled {
		a.verified = r
	}
	return r, err
}
func (a *sidSource) disable(ctx context.Context, b binding) (accountReceipt, error) {
	return a.mutate(ctx, b, disabled)
}
func (a *sidSource) deleteExact(ctx context.Context, b binding) (accountReceipt, error) {
	return a.mutate(ctx, b, deleted)
}
func (a *sidSource) mutate(ctx context.Context, b binding, kind accountKind) (r accountReceipt, err error) {
	if !a.enter(ctx, b) {
		return r, ErrDenied
	}
	defer a.mu.Unlock()
	defer func() {
		if recover() != nil {
			a.unknown = true
			r = accountReceipt{}
			err = ErrUnknown
		}
	}()
	if a.unknown {
		return r, ErrUnknown
	}
	if a.deleted || (kind == deleted && (!a.disabled || !a.verified.complete)) {
		return r, ErrDenied
	}
	if kind == disabled && a.disabled {
		return accountReceipt{a.seal, b, disabled}, nil
	}
	request := sidRequest(b, kind)
	if len(request) == 0 {
		return r, ErrDenied
	}
	// 取得/実行の結果が不明なら再dispatchしない。ownerは部分取得を含め
	// executor自身に残し、この一覧もlifetimeの外へ捨てない。
	a.unknown = true
	owner, issueErr := a.issue()
	if owner != nil {
		a.ownerMu.Lock()
		a.owners = append(a.owners, owner)
		a.ownerMu.Unlock()
	}
	if issueErr != nil || owner == nil {
		return r, ErrUnknown
	}
	if a.stop.Load() {
		return r, ErrUnknown
	}
	result, runErr := owner.execute(ctx, request)
	if runErr != nil || a.stop.Load() || !finite(ctx) || !validSIDResult(request, result) {
		return r, ErrUnknown
	}
	a.unknown = false
	if kind == disabled {
		a.disabled = true
	} else {
		a.deleted = true
	}
	return accountReceipt{a.seal, b, kind}, nil
}

// outer ownerはLifecycleの清掃と別に、未知のchild/pipe/I/Oを回収する。
// 未確認時も参照を残し、再発行や別ownerへの付け替えを認めない。
func (a *sidSource) Close(ctx context.Context) (err error) {
	if a == nil || a.self != a {
		return ErrUnconfigured
	}
	a.stop.Store(true)
	if !finite(ctx) {
		return ErrCleanup
	}
	if a.mu.TryLock() {
		defer a.mu.Unlock()
	} else {
		// 実callは未終了でも、既に保持したownerへ停止を伝える。
		// workerの実returnを回収ackの条件から外さない。
		err = ErrBusy
	}
	defer func() {
		if recover() != nil {
			err = ErrCleanup
		}
	}()
	if !a.ownerMu.TryLock() {
		return ErrBusy
	}
	owners := append([]sidExecutor(nil), a.owners...)
	a.ownerMu.Unlock()
	for _, owner := range owners {
		if owner.Close(ctx) != nil || !finite(ctx) {
			err = ErrCleanup
		}
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	return err
}
