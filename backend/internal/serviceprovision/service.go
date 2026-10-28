package serviceprovision

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// Prepared は固定rootとservice imageの同じ元threadでの保持証拠を所有する。
// 設定の読取成功を、launch tokenやoperator承認へ変換しない。
type Prepared struct {
	loaded   *Loaded
	scope    *bootstrapauth.PreparedService
	record   Record
	accepted atomic.Bool
	poisoned atomic.Bool
}

// PrepareFixed はcallerが固定した元OS threadだけで使用し、partial ownerも返す。
// この内部APIへUI/env/argvのpathを渡す経路は設けない。
func PrepareFixed(ctx context.Context, anchor Anchor) (*Prepared, error) {
	root, err := NewNativeRoot(anchor)
	if err != nil {
		return nil, err
	}
	return root.PrepareService(ctx)
}

// PrepareServiceは同じ固定rootをservice imageの準備検査へ接続する。
// callerはOpenとCloseを含む全期間で元OS threadを保持する。
func (root *Root) PrepareService(ctx context.Context) (*Prepared, error) {
	loaded, err := root.Open(ctx)
	if loaded == nil {
		if err == nil {
			err = ErrRootDenied
		}
		return nil, err
	}
	p := &Prepared{loaded: loaded}
	if err != nil {
		return p, p.reject(err)
	}
	data, err := loaded.Record()
	if err != nil {
		return p, p.reject(err)
	}
	p.record, err = DecodeRecord(data)
	clear(data)
	if err != nil {
		return p, p.reject(err)
	}
	subject, err := loaded.Subject()
	if err != nil {
		return p, p.reject(err)
	}
	if subject.ServiceName != p.record.Profile.ServiceName || subject.ServiceSID != p.record.Profile.Keys.ServiceSID || subject.ProcessUserSID != p.record.Profile.Keys.ProcessUserSID {
		return p, ErrRecord
	}
	token, err := loaded.QueryToken()
	if err != nil {
		return p, p.reject(err)
	}
	factory, err := bootstrapauth.NewNativeFactory(p.record.Profile)
	if err != nil {
		return p, p.reject(err)
	}
	p.scope, err = factory.PrepareService(ctx, token)
	if err != nil {
		return p, p.reject(err)
	}
	if !rootLive(ctx) {
		return p, ErrRootDenied
	}
	p.accepted.Store(true)
	return p, nil
}

// ComposeServiceは保持中の同じrecordを、既存Serviceの構成へ渡す。
// tokenはquery-onlyから昇格せず、別の保護供給元が全回収まで所有する。
// 成功は起動handleの権限やoperator承認の認証を証明せず、OS起動も行わない。
func (p *Prepared) ComposeService(ctx context.Context, token uintptr, approval servicehost.ProtectedProvisioning) (*servicehost.Service, error) {
	if p != nil && p.poisoned.Load() {
		return nil, ErrRootFatal
	}
	if p == nil || !p.accepted.Load() || p.loaded == nil || p.scope == nil || !rootLive(ctx) {
		return nil, ErrRootDenied
	}
	if _, err := p.loaded.QueryToken(); err != nil {
		return nil, p.reject(err)
	}
	if err := p.scope.Recheck(ctx); err != nil {
		return nil, p.reject(err)
	}
	if !rootLive(ctx) || !p.accepted.Load() {
		return nil, ErrRootDenied
	}
	return ComposeService(p.record, token, approval)
}

func (p *Prepared) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.accepted.Store(false)
	var result error
	if p.scope != nil {
		result = p.scope.Close(ctx)
	}
	if p.loaded != nil {
		result = errors.Join(result, p.loaded.Close(ctx))
	}
	return p.reject(result)
}

func (p *Prepared) reject(err error) error {
	p.accepted.Store(false)
	if errors.Is(err, ErrRootFatal) {
		p.poisoned.Store(true)
	}
	if p.poisoned.Load() {
		return errors.Join(ErrRootFatal, err)
	}
	return err
}

// 固定entryの配置値は未設定。環境変数や実行pathから補完しない。
func fixedAnchor() Anchor { return Anchor{} }

// RunFixed は専用executableからだけ使う未設定拒否の入口。
// 起動handleと承認入口の供給・SCM全体の所有回収が未実装の間は、
// native root読取りにもSCMにも入らない。部分取得をos.Exitへ落とさない。
func RunFixed(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrApprovalUnconfigured
	}
	anchor := fixedAnchor()
	if _, err := NewNativeRoot(anchor); err != nil {
		return err
	}
	// QueryTokenをCreateProcessAsUser用の強いhandleと誤認しない。
	return errors.Join(ErrLaunchTokenUnconfigured, ErrApprovalUnconfigured)
}
