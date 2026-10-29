package serviceready

import (
	"context"
	"errors"
	"time"
)

// ServiceWitness は同じthreadのauth scopeとheld service/processのquery所有者です。
// SCM RUNNINGだけを確認し、listener準備・operator認証・承認を証明しません。
// 初回利用後にコピーせず、回収不明でも同じpointerを保持します。
type ServiceWitness struct {
	self  *ServiceWitness
	query *Plane
}

// PrepareServiceWitness は既存のquery-only prefixを使い、listenerを作りません。
// 部分取得時もownerを返すため、errorと同時に返ったpointerもCloseServiceします。
func (f *Factory) PrepareServiceWitness(ctx context.Context) (*ServiceWitness, error) {
	if f == nil || !f.configured {
		return nil, ErrConfig
	}
	if !serviceWitnessLive(ctx) {
		return nil, ErrDenied
	}
	query, err := f.prepareQuery(ctx)
	if query == nil {
		return nil, err
	}
	if err == nil && !serviceWitnessLive(ctx) {
		err = query.invalidate(ErrDenied)
	}
	if err == nil {
		query.prepared = true
	}
	w := &ServiceWitness{query: query}
	w.self = w
	return w, err
}

func serviceWitnessLive(ctx context.Context) bool {
	if !live(ctx) {
		return false
	}
	deadline, _ := ctx.Deadline()
	return time.Now().Before(deadline)
}

// ConfirmServiceRunning はheld serviceのSCM RUNNINGとcurrent PIDを確認します。
// START_PENDINGだけを有限deadline内で待ち、失敗は同じ所有者を失効させます。
func (w *ServiceWitness) ConfirmServiceRunning(ctx context.Context) error {
	return w.checkService(ctx, true)
}

// RecheckService はRUNNING確認済みの同じquery証拠を照合します。
// 確認前の呼出しやRUNNINGからの後退もstickyに拒否します。
func (w *ServiceWitness) RecheckService(ctx context.Context) error {
	return w.checkService(ctx, false)
}

func (w *ServiceWitness) checkService(ctx context.Context, wait bool) (err error) {
	if w == nil || w.self != w || w.query == nil {
		return ErrDenied
	}
	q := w.query
	defer func() {
		if err != nil {
			q.invalidate(err)
		}
	}()
	if q.poisoned.Load() {
		return ErrFatal
	}
	if !serviceWitnessLive(ctx) || !q.prepared || q.revoked.Load() || q.stopped.Load() {
		return ErrDenied
	}
	if !admit(ctx, &q.gate) {
		return ErrDenied
	}
	defer q.gate.Unlock()
	for {
		if q.poisoned.Load() {
			return ErrFatal
		}
		if !serviceWitnessLive(ctx) || q.revoked.Load() || q.stopped.Load() || !wait && !q.running {
			return ErrDenied
		}
		state, err := q.verify(ctx, false)
		if err != nil {
			return err
		}
		if q.poisoned.Load() {
			return ErrFatal
		}
		if !serviceWitnessLive(ctx) || q.revoked.Load() || q.stopped.Load() {
			return ErrDenied
		}
		if state.state == 4 {
			q.running = true
			return nil
		}
		pause(ctx)
	}
}

// CloseService は同じquery参照を有限回収し、unknownなら再試行用に保持します。
func (w *ServiceWitness) CloseService(ctx context.Context) error {
	if w == nil || w.query == nil {
		return nil
	}
	if w.self != w {
		return errors.Join(ErrDenied, ErrUnconfirmed)
	}
	return w.query.Close(ctx)
}
