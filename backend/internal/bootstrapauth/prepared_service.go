package bootstrapauth

import "context"

// PreparedService は RUNNING 前の service 自主体と固定 image だけを保持します。
// SCM 状態、listener、operator、peer、承認、grant の証明ではありません。
// ServiceScope/Peer への変換や、それらを要求する経路への代用は提供しません。
type PreparedService struct {
	s    *scope
	pid  uint32
	gate chan struct{}
}

// PrepareService は caller 所有の固定 OS thread で、既存 primary token を借用します。
// 部分取得後は失敗時も PreparedService を返し、Close による回収を要求します。
func (f *Factory) PrepareService(ctx context.Context, borrowedPrimaryToken uintptr) (*PreparedService, error) {
	if f == nil || !f.configured {
		return nil, ErrConfig
	}
	if !live(ctx) || !realHandle(borrowedPrimaryToken) {
		return nil, ErrDenied
	}
	s, err := f.begin(ctx)
	if err != nil {
		return nil, err
	}
	s.token = borrowedPrimaryToken
	prepared := &PreparedService{s: s, gate: make(chan struct{}, 1)}
	err = s.openImage(ctx, s.p.SupervisorImage)
	if err == nil {
		prepared.pid = s.api.currentPID()
		err = prepared.verify(ctx)
	}
	return prepared, prepared.finish(err)
}
func (p *PreparedService) finish(err error) error {
	if final := p.s.subject(); final != nil {
		err = final
	}
	p.s.accepted = err == nil
	if err == ErrFatal {
		p.s.poisoned = true
	}
	return err
}
func (p *PreparedService) verify(ctx context.Context) error {
	if err := p.s.subject(); err != nil {
		return err
	}
	if !live(ctx) {
		return ErrDenied
	}
	if err := p.self(); err != nil {
		return err
	}
	for _, image := range p.s.images {
		if err := image.verify(ctx, p.s); err != nil {
			return err
		}
	}
	if err := p.self(); err != nil {
		return err
	}
	if err := p.s.subject(); err != nil {
		return err
	}
	if !live(ctx) {
		return ErrDenied
	}
	return nil
}
func (p *PreparedService) self() error {
	if p.pid == 0 || p.s.api.currentPID() != p.pid {
		return ErrDenied
	}
	path, err := p.s.api.currentImage()
	if err != nil || path != p.s.p.SupervisorImage.Path {
		return denied(err)
	}
	return p.s.borrowedIdentity()
}

// Recheck は準備の同じ自主体・固定 image を確認し、SCM RUNNING を要求しません。
// admission の待機だけを context で打ち切り、worker を追加しません。
func (p *PreparedService) Recheck(ctx context.Context) error {
	if p == nil || p.s == nil || p.gate == nil {
		return ErrClosed
	}
	if !p.enter(ctx) {
		return ErrDenied
	}
	defer p.leave()
	if p.s.poisoned {
		return ErrFatal
	}
	if p.s.closed {
		return ErrClosed
	}
	if !p.s.accepted {
		return ErrDenied
	}
	return p.finish(p.verify(ctx))
}
func (p *PreparedService) enter(ctx context.Context) bool {
	if !live(ctx) {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case p.gate <- struct{}{}:
		if !live(ctx) {
			p.leave()
			return false
		}
		return true
	}
}
func (p *PreparedService) leave() { <-p.gate }

// Close は既所有 image handle の回収だけを行い、RUNNING の確認を行いません。
func (p *PreparedService) Close(ctx context.Context) error {
	if p == nil || p.s == nil || p.gate == nil {
		return nil
	}
	if !p.enter(ctx) {
		return ErrUnconfirmed
	}
	defer p.leave()
	// scope はこの型だけが所有し、他の scope 呼出しは gate 内に限ります。
	return closeScope(ctx, p.s)
}

// CheckServiceCaller は SCM entry の元 caller を移送前に拒否する service 用 guard です。
// operator 認証には使えず、成功しても ready/RUNNING/server/承認の証明を返しません。
// owned handle を開かず、移送先での PrepareService の全検証を省略できません。
func (f *Factory) CheckServiceCaller(ctx context.Context) (err error) {
	if f == nil || !f.configured {
		return ErrConfig
	}
	if !live(ctx) {
		return ErrDenied
	}
	s, err := f.begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if final := s.subject(); final != nil {
			err = final
		}
		if err == nil && !live(ctx) {
			err = ErrDenied
		}
	}()
	if !live(ctx) {
		return ErrDenied
	}
	if s.api.currentPID() == 0 {
		return ErrDenied
	}
	if !live(ctx) {
		return ErrDenied
	}
	path, err := s.api.currentImage()
	if err != nil || path != s.p.SupervisorImage.Path {
		return denied(err)
	}
	return nil
}
