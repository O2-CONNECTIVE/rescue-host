package servicehost

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// NativeBootstrapConfig は保護された配置元だけが構成する固定値です。
// Baseのstdio生成/保護とprimary tokenの取得・所有は供給元の責務です。
type NativeBootstrapConfig struct {
	Profile      bootstrapauth.Profile
	PrimaryToken uintptr
	Backend      bootstrap.Config
	HostKeyRef   string
	Limits       stdio.Limits
}

type serviceProbe interface {
	Recheck(context.Context) error
	Close(context.Context) error
}
type bootstrapPair interface {
	ChildReadHandle() uintptr
	Publish(context.Context, []byte) error
	ReleaseChildRead(context.Context) error
	Close(context.Context) error
}
type nativeBootstrapCalls struct {
	verify     func(context.Context, uintptr) (serviceProbe, error)
	pair       func(context.Context, bootstrapio.Setup) (bootstrapPair, error)
	closeChild func(context.Context, *childjob.Handles, uintptr) error
}
type nativeChildProvisioning struct {
	mu   sync.Mutex
	used bool
	base ProtectedChildProvisioning
	cfg  NativeBootstrapConfig
	api  nativeBootstrapCalls
}

// NewNativeChildProvisioning は既存stdio provisionerへ具体的bootstrap搬送を追加します。
// Baseが返すchild-side stdio複製はこのadapterへ所有を移し、再度閉じてはいけません。
// parentのstdio streamはControlが所有し、primary tokenは借用のままです。
// 未設定main/SCMからは構成せず、ここでサービス登録や承認は行いません。
func NewNativeChildProvisioning(base ProtectedChildProvisioning, c NativeBootstrapConfig) (ProtectedChildProvisioning, error) {
	if base == nil || !ordinaryNativeHandle(c.PrimaryToken) || c.HostKeyRef == "" || c.Limits.CleanupTimeout <= 0 {
		return nil, ErrProvisioningUnconfigured
	}
	if source, ok := base.(*nativeStdioProvisioning); ok && (source == nil || source.identity != stdioIdentityFrom(c.Profile, c.PrimaryToken)) {
		return nil, ErrProvisioningRejected
	}
	f, err := bootstrapauth.NewNativeFactory(c.Profile)
	if err != nil {
		return nil, ErrProvisioningUnconfigured
	}
	if _, ok := c.Profile.Keys.Catalog[c.HostKeyRef]; !ok {
		return nil, ErrProvisioningRejected
	}
	c.Backend.AllowedKeys = slices.Clone(c.Backend.AllowedKeys)
	return &nativeChildProvisioning{base: base, cfg: c, api: nativeBootstrapCalls{
		verify: func(ctx context.Context, t uintptr) (serviceProbe, error) { return f.VerifyService(ctx, t) },
		pair: func(ctx context.Context, s bootstrapio.Setup) (bootstrapPair, error) {
			return bootstrapio.NewNativePair(ctx, s)
		},
		closeChild: closeNativeChildPipes,
	}}, nil
}

func ordinaryNativeHandle(h uintptr) bool { return h != 0 && h < ^uintptr(15) }

// issuer contextを持たない旧入口からnative起動へfallbackしません。
func (*nativeChildProvisioning) PrepareChild(context.Context, supervisor.ProvisionedGrant) (ChildPlan, error) {
	return ChildPlan{}, ErrProvisioningUnconfigured
}

func (n *nativeChildProvisioning) prepareIssued(ctx context.Context, a *supervisor.Authority, i IssuedLaunch) (ChildPlan, error) {
	n.mu.Lock()
	if n.used {
		n.mu.Unlock()
		return ChildPlan{}, ErrAlreadyUsed
	}
	n.used = true
	n.mu.Unlock()
	if !controlBudget(ctx) || a == nil || i.instance == ([32]byte{}) {
		return ChildPlan{}, ErrProvisioningRejected
	}
	snapshot := a.Snapshot(time.Now())
	if snapshot.State != supervisor.Granted || snapshot.Grant.Binding != i.binding {
		return ChildPlan{}, ErrProvisioningRejected
	}
	p := &nativePublication{authority: a, grant: snapshot.Grant, cleanup: make(chan struct{}, 1), token: n.cfg.PrimaryToken, api: n.api, budget: n.cfg.Limits.CleanupTimeout}
	p.cleanup <- struct{}{}
	plan := ChildPlan{PreparedBootstrap: p}
	var err error
	p.probe, err = n.api.verify(ctx, n.cfg.PrimaryToken)
	if err != nil || p.probe == nil {
		return plan, errors.Join(ErrProvisioningRejected, err)
	}
	var base ChildPlan
	if source, ok := n.base.(*nativeStdioProvisioning); ok {
		base, err = source.prepareStdio(ctx, snapshot.Grant, p.probe)
	} else {
		base, err = n.base.PrepareChild(ctx, snapshot.Grant)
	}
	p.children = base.Spec.Handles
	p.stdio = base.Stdio
	p.extra = base.PreparedBootstrap
	base.PreparedBootstrap = p
	plan = base
	if err != nil {
		return plan, err
	}
	if base.Control == nil || base.Bootstrap != nil || p.extra != nil || base.Spec.Bootstrap != nil || base.Spec.PrimaryToken != n.cfg.PrimaryToken || base.Spec.Artifact.Path != n.cfg.Profile.ChildImage.Path || base.Spec.Artifact.SHA256 != n.cfg.Profile.ChildImage.SHA256 {
		return plan, ErrProvisioningRejected
	}
	wire := base.Control.config.Wire
	message := bootstrap.Message{IssuerInstanceEpoch: hex.EncodeToString(i.instance[:]), Grant: snapshot.Grant, Wire: wire, Config: n.cfg.Backend, HostKeyRef: n.cfg.HostKeyRef, Limits: n.cfg.Limits}
	p.record, err = bootstrap.EncodeRecord(message)
	if err != nil || ctx.Err() != nil || a.Snapshot(time.Now()).State != supervisor.Granted {
		return plan, errors.Join(ErrProvisioningRejected, err)
	}
	p.pipe, err = n.api.pair(ctx, bootstrapio.Setup{UserSID: n.cfg.Profile.Keys.ProcessUserSID, ServiceSID: n.cfg.Profile.Keys.ServiceSID, CleanupTimeout: n.cfg.Limits.CleanupTimeout})
	if err != nil || p.pipe == nil {
		return plan, errors.Join(ErrProvisioningRejected, err)
	}
	plan.Spec.Bootstrap = &childjob.Bootstrap{ReadHandle: p.pipe.ChildReadHandle(), Values: bootstrap.LaunchValues{IssuerInstanceEpoch: message.IssuerInstanceEpoch, Wire: wire}}
	if !ordinaryNativeHandle(plan.Spec.Bootstrap.ReadHandle) {
		return plan, ErrProvisioningRejected
	}
	return plan, nil
}

type nativePublication struct {
	mu           sync.Mutex
	used, closed bool
	cleanup      chan struct{}
	authority    *supervisor.Authority
	grant        supervisor.ProvisionedGrant
	record       []byte
	probe        serviceProbe
	pipe         bootstrapPair
	children     childjob.Handles
	stdio        *OwnedStdio
	token        uintptr
	api          nativeBootstrapCalls
	budget       time.Duration
	extra        PreparedBootstrap
}

func (p *nativePublication) Publish(ctx context.Context, a *supervisor.Authority) (err error) {
	p.mu.Lock()
	if p.used || p.closed {
		p.mu.Unlock()
		return ErrAlreadyUsed
	}
	p.used = true
	p.mu.Unlock()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), p.budget)
		defer cancel()
		if p.probe != nil {
			err = errors.Join(err, p.probe.Close(cleanup))
		}
		if err != nil {
			_ = p.authority.Revoke(p.grant.Binding)
			if p.pipe != nil {
				err = errors.Join(err, p.pipe.Close(cleanup))
			}
		}
	}()
	if !controlBudget(ctx) || a != p.authority || p.pipe == nil || p.probe == nil {
		return ErrProvisioningRejected
	}
	snapshot := a.Snapshot(time.Now())
	if snapshot.State != supervisor.Granted || !sameGrant(snapshot.Grant, p.grant) {
		return ErrProvisioningRejected
	}
	if err = p.probe.Recheck(ctx); err != nil {
		return err
	}
	if err = p.releaseChildren(ctx); err != nil {
		return err
	}
	if err = p.pipe.Publish(ctx, p.record); err != nil {
		return err
	}
	if err = p.probe.Recheck(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || ctx.Err() != nil || a.Snapshot(time.Now()).State != supervisor.Granted {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (p *nativePublication) releaseChildren(ctx context.Context) error {
	select {
	case <-p.cleanup:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	defer func() { p.cleanup <- struct{}{} }()
	var err error
	if p.stdio != nil {
		err = p.stdio.releaseChild(ctx)
	} else {
		err = p.api.closeChild(ctx, &p.children, p.token)
	}
	if p.pipe != nil {
		err = errors.Join(err, p.pipe.ReleaseChildRead(ctx))
	}
	return err
}

func (p *nativePublication) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.authority != nil {
		_ = p.authority.Revoke(p.grant.Binding)
	}
	p.mu.Lock()
	p.closed = true
	p.used = true
	p.mu.Unlock()
	if !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	select {
	case <-p.cleanup:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	defer func() { p.cleanup <- struct{}{} }()
	var err error
	if p.stdio != nil {
		err = p.stdio.Close(ctx)
	} else {
		err = p.api.closeChild(ctx, &p.children, p.token)
	}
	if p.pipe != nil {
		err = errors.Join(err, p.pipe.Close(ctx))
	}
	if p.probe != nil {
		err = errors.Join(err, p.probe.Close(ctx))
	}
	if p.extra != nil {
		err = errors.Join(err, p.extra.Close(ctx))
	}
	return err
}

func sameGrant(a, b supervisor.ProvisionedGrant) bool {
	return a.Binding == b.Binding && a.KeySetDigest == b.KeySetDigest && a.GuestBoundary == b.GuestBoundary && a.HardDeadline.Equal(b.HardDeadline) && a.BackendLeaseCeiling == b.BackendLeaseCeiling
}
