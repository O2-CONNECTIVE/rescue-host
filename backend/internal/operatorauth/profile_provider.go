package operatorauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// policy は候補照会より先に保護配置元から固定する。空欄を観測値で補わない。
// primary と message の型・level は別入力であり、相互変換しない。
type profileProviderPolicy struct {
	material  profilePipeMaterial
	primary   tokenProjection
	imagePath string
}

// snapshot は候補の照会結果であり、operator 認証や正規発行の証拠ではない。
type profileCandidateSnapshot struct {
	pid                 uint32
	creation            uint64
	imagePath           string
	primary             tokenProjection
	tokenID, modifiedID uint64
}

// 正規 lease 発行元は未接続。この private seam を公開の成功 callback にしない。
type profileCandidateQuery interface {
	inspect(context.Context) (profileCandidateSnapshot, error)
	close(context.Context) error
}

type protectedProfileProvider struct {
	mu                     sync.Mutex
	policy                 profileProviderPolicy
	query                  profileCandidateQuery
	baseline               profileCandidateSnapshot
	request                context.Context
	used, prepared, closed bool
}

func newProtectedProfileProvider(p profileProviderPolicy, q profileCandidateQuery) (*protectedProfileProvider, error) {
	h := p.material.profile.operator
	want := h
	want.kind, want.level = 1, 0
	_, err := encodeHelperProfileFrame(p.material.launchID, p.material.profile)
	if err != nil || p.primary != want || p.material.cleanup <= 0 || p.material.cleanup > time.Minute || !localHelperImage(p.imagePath) || q == nil {
		return nil, ErrUnconfigured
	}
	return &protectedProfileProvider{policy: p, query: q}, nil
}

func (p *protectedProfileProvider) prepare(ctx context.Context) (profilePipeMaterial, error) {
	if p == nil || !p.mu.TryLock() {
		return profilePipeMaterial{}, ErrDenied
	}
	defer p.mu.Unlock()
	if p.used || p.closed || !reportContextValid(ctx) {
		return profilePipeMaterial{}, ErrDenied
	}
	p.used = true
	if p.request == nil {
		p.request = ctx
	}
	actual, err := p.query.inspect(ctx)
	if err != nil || actual.pid == 0 || actual.creation == 0 || actual.tokenID == 0 || actual.modifiedID == 0 || actual.imagePath != p.policy.imagePath || actual.primary != p.policy.primary || !reportContextValid(ctx) || !reportContextValid(p.request) {
		return profilePipeMaterial{}, errors.Join(ErrDenied, err)
	}
	p.baseline = actual
	p.prepared = true
	return p.policy.material, nil
}

func (p *protectedProfileProvider) recheck(ctx context.Context) error {
	if p == nil || !p.mu.TryLock() {
		return ErrDenied
	}
	defer p.mu.Unlock()
	if p.closed || !reportContextValid(ctx) || p.used && !reportContextValid(p.request) {
		p.prepared = false
		return ErrDenied
	}
	if !p.used {
		return nil
	} // 起動前のscope検査。候補照会や認証成功を意味しない。
	if !p.prepared {
		return ErrDenied
	}
	actual, err := p.query.inspect(ctx)
	if err != nil || actual != p.baseline || !reportContextValid(ctx) || !reportContextValid(p.request) {
		p.prepared = false
		return errors.Join(ErrDenied, err)
	}
	return nil
}

// Batchは起動専用contextを作る前の元要求を渡す。prepare単体ではそのctxを使う。
func (p *protectedProfileProvider) bindRequest(ctx context.Context) error {
	if p == nil || !p.mu.TryLock() {
		return ErrDenied
	}
	defer p.mu.Unlock()
	if p.used || p.closed || p.request != nil || !reportContextValid(ctx) {
		return ErrDenied
	}
	p.request = ctx
	return nil
}

func (p *protectedProfileProvider) close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if !p.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer p.mu.Unlock()
	p.closed = true
	return p.query.close(ctx)
}
