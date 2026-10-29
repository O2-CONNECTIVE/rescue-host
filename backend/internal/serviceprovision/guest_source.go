package serviceprovision

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount"
)

const guestRecordName = "guest-provisioning-v1.json"
const guestDigestName = "guest-provisioning-v1.sha256"

// GuestConfiguration は元service ownerが保護record/digestを実読取した所有物。
// decode値や成功callbackから発行しない。実token/承認/実行許可の証明ではない。
type GuestConfiguration struct {
	self                       *GuestConfiguration
	prepared                   *Prepared
	loaded                     *Loaded
	layout                     *guestaccount.FixedLayout
	baseDigest                 [32]byte
	mu                         sync.Mutex
	accepted, closed, poisoned atomic.Bool
}

// 追加配置は既存固定anchorのsiblingだけ。UI/env/実行場所からpathを受けない。
func guestAnchor(base Anchor) (Anchor, error) {
	if e := base.Validate(); e != nil {
		return Anchor{}, e
	}
	parent := base.RecordPath[:strings.LastIndex(base.RecordPath, `\`)+1]
	out := base
	out.RecordPath = parent + guestRecordName
	out.DigestPath = parent + guestDigestName
	for _, a := range []string{out.RecordPath, out.DigestPath} {
		for _, b := range []string{base.RecordPath, base.DigestPath} {
			if strings.EqualFold(a, b) {
				return Anchor{}, ErrRootConfig
			}
		}
	}
	return out, out.Validate()
}

// LoadFixedGuestは既存Rootの全祖先保持・主体・digest・実EOF検査を使う実入口。
// partial ownerも返す。callerは元threadと既存Preparedを全回収まで保持する。
func (p *Prepared) LoadFixedGuest(ctx context.Context) (*GuestConfiguration, error) {
	if p == nil || p.loaded == nil || !p.accepted.Load() || !rootLive(ctx) {
		return nil, ErrRootDenied
	}
	anchor, e := guestAnchor(p.loaded.anchor)
	if e != nil {
		return nil, e
	}
	root, e := NewNativeRoot(anchor)
	if e != nil {
		return nil, e
	}
	return p.loadFixedGuest(ctx, root)
}
func (p *Prepared) loadFixedGuest(ctx context.Context, root *Root) (owner *GuestConfiguration, result error) {
	if p == nil || p.loaded == nil || p.scope == nil || !p.accepted.Load() || p.poisoned.Load() || !rootLive(ctx) || root == nil {
		return nil, ErrRootDenied
	}
	expected, e := guestAnchor(p.loaded.anchor)
	if e != nil || root.anchor != expected {
		return nil, ErrRootConfig
	}
	if _, e = p.loaded.QueryToken(); e != nil {
		return nil, p.reject(e)
	}
	if e = p.scope.Recheck(ctx); e != nil {
		return nil, p.reject(e)
	}
	base, e := p.loaded.Record()
	if e != nil {
		return nil, p.reject(e)
	}
	record, e := DecodeRecord(base)
	if e != nil {
		return nil, e
	}
	g := &GuestConfiguration{prepared: p, baseDigest: sha256.Sum256(base)}
	g.self = g
	owner = g
	defer func() {
		if recover() != nil {
			g.poisoned.Store(true)
			g.accepted.Store(false)
			result = ErrRootFatal
		}
	}()
	g.loaded, e = root.Open(ctx)
	if e != nil {
		return g, g.reject(e)
	}
	data, e := g.loaded.Record()
	if e != nil {
		return g, g.reject(e)
	}
	g.layout, e = guestaccount.DecodeFixedLayout(data)
	clear(data)
	if e != nil {
		return g, g.reject(e)
	}
	subject, e := g.loaded.Subject()
	if e != nil {
		return g, g.reject(e)
	}
	keys := record.Profile.Keys
	if subject.ServiceName != record.Profile.ServiceName || subject.ProcessUserSID != keys.ProcessUserSID || subject.ServiceSID != keys.ServiceSID || !g.layout.MatchesService(record.Profile.ServiceName, keys.ProcessUserSID, keys.ServiceSID, keys.OperatorSID) {
		return g, g.reject(ErrRecord)
	}
	// 任意roleの件数だけではなく、実際の両record/digestと固定image/keyを要求する。
	for _, loaded := range []*Loaded{p.loaded, g.loaded} {
		for _, held := range loaded.held {
			if held.leaf && !g.layout.IncludesConfiguration(held.path, held.before.id) {
				return g, g.reject(ErrRecord)
			}
		}
	}
	for _, path := range []string{record.Profile.SupervisorImage.Path, record.Profile.ChildImage.Path} {
		if !g.layout.IncludesArtifact(path) {
			return g, g.reject(ErrRecord)
		}
	}
	for _, key := range keys.Catalog {
		if !g.layout.IncludesHostKey(key.Path) {
			return g, g.reject(ErrRecord)
		}
	}
	g.accepted.Store(true)
	if e = g.recheck(ctx); e != nil {
		return g, g.reject(e)
	}
	return g, nil
}
func (g *GuestConfiguration) reject(e error) error {
	g.accepted.Store(false)
	if errors.Is(e, ErrRootFatal) {
		g.poisoned.Store(true)
	}
	if g.poisoned.Load() {
		return errors.Join(ErrRootFatal, e)
	}
	return e
}
func (g *GuestConfiguration) valid() bool {
	return g != nil && g.self == g && g.prepared != nil && g.loaded != nil
}
func (g *GuestConfiguration) recheck(ctx context.Context) error {
	if !g.valid() || !g.accepted.Load() || g.closed.Load() || !rootLive(ctx) {
		return ErrRootDenied
	}
	if g.poisoned.Load() || g.prepared.poisoned.Load() {
		return ErrRootFatal
	}
	if !g.prepared.accepted.Load() {
		return ErrRootDenied
	}
	// 元image scopeのgateを再入しない。後段のPreparedLeaseがimageを保持する。
	// ここは両方のroot/recordの主体と保持同一性だけを元threadで実照合する。
	for _, loaded := range []*Loaded{g.prepared.loaded, g.loaded} {
		if e := guestRecheckRoot(ctx, loaded); e != nil {
			return e
		}
	}
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	return nil
}

func guestRecheckRoot(ctx context.Context, loaded *Loaded) error {
	if e := loaded.get(); e != nil {
		return e
	}
	defer loaded.gate.Unlock()
	return loaded.recheckHeld(ctx)
}

// Layoutはdecode結果だけを返す。sourceの承認・immutable grantは別の所有者が扱う。
func (g *GuestConfiguration) Layout(ctx context.Context) (*guestaccount.FixedLayout, error) {
	if !g.valid() || !g.mu.TryLock() {
		return nil, ErrRootDenied
	}
	defer g.mu.Unlock()
	if e := g.recheck(ctx); e != nil {
		return nil, g.reject(e)
	}
	return g.layout, nil
}

// Digestは両保護設定bytesへ束縛する候補値。UI確認/grantへ自動採用しない。
func (g *GuestConfiguration) Digest(ctx context.Context) ([32]byte, error) {
	if !g.valid() || !g.mu.TryLock() {
		return [32]byte{}, ErrRootDenied
	}
	defer g.mu.Unlock()
	if e := g.recheck(ctx); e != nil {
		return [32]byte{}, g.reject(e)
	}
	data := append([]byte("rescue-fixed-guest-bundle/1\x00"), g.baseDigest[:]...)
	other := g.layout.Digest()
	data = append(data, other[:]...)
	return sha256.Sum256(data), nil
}
func (g *GuestConfiguration) Close(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if g.self != g {
		return ErrRootDenied
	}
	g.accepted.Store(false)
	if !rootLive(ctx) || !g.mu.TryLock() {
		return ErrRootUnconfirmed
	}
	defer g.mu.Unlock()
	if g.closed.Load() {
		return nil
	}
	if g.loaded != nil {
		if e := g.loaded.Close(ctx); e != nil {
			return g.reject(e)
		}
	}
	if !rootLive(ctx) {
		return ErrRootUnconfirmed
	}
	g.closed.Store(true)
	return nil
}
