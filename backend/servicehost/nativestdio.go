package servicehost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// NativeStdioConfig は保護配置元の固定profile/既存token/予算だけを受け取る。
// 署名鍵の読取り・新しい認可・service設定は行わない。
type NativeStdioConfig struct {
	Profile                                        bootstrapauth.Profile
	PrimaryToken                                   uintptr
	PolicyRef                                      string
	PollInterval, OperationTimeout, CleanupTimeout time.Duration
	StderrBytes                                    uint64
}

type nativeStdioProvisioning struct {
	mu       sync.Mutex
	used     bool
	cfg      NativeStdioConfig
	identity stdioIdentity
	open     func(context.Context, childstdio.Setup) (stdioPipes, error)
}

// pipe作成に使う認証対象だけを値として固定する。ACL/catalogの権威はwrapperのscopeに残す。
type stdioIdentity struct {
	user, service, name string
	supervisor, child   childjob.Artifact
	token               uintptr
}

func stdioIdentityFrom(p bootstrapauth.Profile, t uintptr) stdioIdentity {
	return stdioIdentity{user: p.Keys.ProcessUserSID, service: p.Keys.ServiceSID, name: p.ServiceName, supervisor: childjob.Artifact{Path: p.SupervisorImage.Path, SHA256: p.SupervisorImage.SHA256}, child: childjob.Artifact{Path: p.ChildImage.Path, SHA256: p.ChildImage.SHA256}, token: t}
}

func NewNativeStdioProvisioning(c NativeStdioConfig) (ProtectedChildProvisioning, error) {
	if !ordinaryNativeHandle(c.PrimaryToken) || c.PolicyRef == "" || len(c.PolicyRef) > 128 || c.PollInterval <= 0 || c.OperationTimeout <= 0 || c.CleanupTimeout <= 0 || c.StderrBytes == 0 {
		return nil, ErrProvisioningUnconfigured
	}
	if _, err := bootstrapauth.NewNativeFactory(c.Profile); err != nil {
		return nil, ErrProvisioningRejected
	}
	identity := stdioIdentityFrom(c.Profile, c.PrimaryToken)
	// 未使用のmutable profile/mapを保持して後から信頼判断へ使わない。
	c.Profile = bootstrapauth.Profile{}
	return &nativeStdioProvisioning{cfg: c, identity: identity, open: func(ctx context.Context, s childstdio.Setup) (stdioPipes, error) { return childstdio.NewNative(ctx, s) }}, nil
}

// 裸のbaseをlegacy起動のbootstrapへ転用しない。
func (*nativeStdioProvisioning) PrepareChild(context.Context, supervisor.ProvisionedGrant) (ChildPlan, error) {
	return ChildPlan{}, ErrProvisioningUnconfigured
}

// この入口はnative wrapperが保持する同じservice認証scopeだけを受け取る。
// stdio作成を元threadの照合後に限定し、任意callbackやUIの認証済みboolを受け付けない。
func (n *nativeStdioProvisioning) prepareStdio(ctx context.Context, g supervisor.ProvisionedGrant, probe serviceProbe) (ChildPlan, error) {
	if n == nil {
		return ChildPlan{}, ErrProvisioningUnconfigured
	}
	n.mu.Lock()
	if n.used {
		n.mu.Unlock()
		return ChildPlan{}, ErrAlreadyUsed
	}
	n.used = true
	n.mu.Unlock()
	if !controlBudget(ctx) || probe == nil || n.open == nil || g.GuestBoundary != supervisor.SeparatedNonAdminGuest || g.KeySetDigest == ([32]byte{}) || !time.Now().Before(g.HardDeadline) || n.cfg.PollInterval > g.BackendLeaseCeiling {
		return ChildPlan{}, ErrProvisioningRejected
	}
	ctx, cancel := context.WithDeadline(ctx, g.HardDeadline)
	defer cancel()
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ChildPlan{}, ErrProvisioningRejected
	}
	wire := control.WireContext{Generation: strconv.FormatUint(g.Binding.Generation, 10), Identity: control.WireIdentity{LaunchID: g.Binding.Identity.LaunchID, Epoch: g.Binding.Identity.Epoch, PolicyDigest: g.Binding.Identity.PolicyDigest}, Binding: control.Binding{ArtifactSHA256: hex.EncodeToString(n.identity.child.SHA256[:]), ChannelNonce: hex.EncodeToString(nonce[:])}, PolicyRef: n.cfg.PolicyRef}
	if _, err := control.NewState(wire); err != nil {
		return ChildPlan{}, ErrProvisioningRejected
	}
	if err := probe.Recheck(ctx); err != nil || ctx.Err() != nil {
		return ChildPlan{}, errors.Join(ErrProvisioningRejected, err)
	}
	pipes, openErr := n.open(ctx, childstdio.Setup{UserSID: n.identity.user, ServiceSID: n.identity.service, CleanupTimeout: n.cfg.CleanupTimeout})
	plan := ChildPlan{Spec: childjob.Spec{Artifact: n.identity.child, PrimaryToken: n.cfg.PrimaryToken, CleanupTimeout: n.cfg.CleanupTimeout}}
	if pipes != nil {
		if openErr != nil {
			plan.Stdio = retainedStdio(pipes, n.cfg.StderrBytes)
		} else {
			var ownErr error
			plan.Stdio, ownErr = newOwnedStdio(pipes, n.cfg.StderrBytes)
			openErr = errors.Join(openErr, ownErr)
		}
		plan.Spec.Handles = pipes.ChildHandles()
	}
	if openErr != nil || plan.Stdio == nil {
		return plan, errors.Join(ErrProvisioningRejected, openErr)
	}
	if err := probe.Recheck(ctx); err != nil || ctx.Err() != nil || !time.Now().Before(g.HardDeadline) {
		return plan, errors.Join(ErrProvisioningRejected, err)
	}
	var err error
	plan.Control, err = NewOwnedControl(OwnedControlConfig{Wire: wire, Input: pipes.Input(), Output: pipes.Output(), PollInterval: n.cfg.PollInterval, OperationTimeout: n.cfg.OperationTimeout})
	if err != nil {
		return plan, ErrProvisioningRejected
	}
	return plan, nil
}
