package serviceready

import "context"

// ProcessBinding は保持processの照合値で、単独では認証や所有権を表しません。
type ProcessBinding struct {
	PID      uint32
	Creation uint64
}

// Preflight は元thread・自主体・固定service設定とprocess保護のquery-only所有scopeです。
// listener、SCM RUNNING、operator/server認証、承認の証明ではありません。
type Preflight struct{ query *Plane }

// PreparePreflight はquery-only primary tokenも借用でき、強いtokenを新規に開きません。
// 部分取得時はerrorと同時にownerを返すため、callerは失敗時もCloseを行います。
func (f *Factory) PreparePreflight(ctx context.Context) (*Preflight, error) {
	query, err := f.prepareQuery(ctx)
	if query == nil {
		return nil, err
	}
	if err == nil {
		query.prepared = true
	}
	return &Preflight{query: query}, err
}
func (p *Preflight) Recheck(ctx context.Context) error { _, err := p.Binding(ctx); return err }

// Binding は同じopaque scopeを再検査し、gate内で保持generationの値を取り出します。
// 値を複製しても、別のtoken/handleの取得元や権限の証明にはなりません。
func (p *Preflight) Binding(ctx context.Context) (binding ProcessBinding, err error) {
	if p == nil || p.query == nil {
		return binding, ErrDenied
	}
	q := p.query
	defer func() {
		if err != nil {
			q.invalidate(err)
			binding = ProcessBinding{}
		}
	}()
	if q.poisoned.Load() {
		return binding, ErrFatal
	}
	if !live(ctx) || !q.prepared || q.revoked.Load() || q.stopped.Load() {
		return binding, ErrDenied
	}
	if !admit(ctx, &q.gate) {
		return binding, ErrDenied
	}
	defer q.gate.Unlock()
	if q.poisoned.Load() {
		return binding, ErrFatal
	}
	if q.revoked.Load() || q.stopped.Load() {
		return binding, ErrDenied
	}
	if _, err = q.verify(ctx, false); err != nil {
		return binding, err
	}
	if q.revoked.Load() || q.stopped.Load() {
		return binding, ErrDenied
	}
	return ProcessBinding{PID: q.pid, Creation: q.creation}, nil
}
func (p *Preflight) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.query.Close(ctx)
}
