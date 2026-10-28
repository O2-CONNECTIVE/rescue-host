package servicehost

import (
	"context"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// ChildPlanは未決の保護bootstrapが準備する値。任意path/tokenや新wireを採用しない。
// Bootstrapは同じ起動へ型付きConfigを安全に渡す未実装依存。
// 借用child-side pipe/tokenはprovisionerが所有し、startup完了/取消時に不要な複製を閉じる。
// 識別子やhashだけを相手認証へ読み替えてはならない。
type ChildPlan struct {
	Spec      childjob.Spec
	Control   *OwnedControl
	Bootstrap func(context.Context, supervisor.Binding) error
}

type ProtectedChildProvisioning interface {
	PrepareChild(context.Context, supervisor.ProvisionedGrant) (ChildPlan, error)
}

type JobRuntime struct{ source ProtectedChildProvisioning }

// NewJobRuntimeはadapterを構成するだけで、OSの設定・token取得・process起動はしない。
// default SCM/mainはsourceを設定せず、実機起動には別のaction-time承認を要する。
func NewJobRuntime(source ProtectedChildProvisioning) *JobRuntime { return &JobRuntime{source: source} }

func (r *JobRuntime) Start(ctx context.Context, authority *supervisor.Authority) (Process, error) {
	if r == nil || r.source == nil {
		return nil, ErrProvisioningUnconfigured
	}
	if ctx == nil || ctx.Err() != nil || authority == nil {
		return nil, ErrProvisioningRejected
	}
	if _, finite := ctx.Deadline(); !finite {
		return nil, ErrProvisioningRejected
	}
	snapshot := authority.Snapshot(time.Now())
	if snapshot.State != supervisor.Granted {
		return nil, ErrProvisioningRejected
	}
	plan, err := r.source.PrepareChild(ctx, snapshot.Grant)
	var owned *jobProcess
	if plan.Control != nil {
		owned = &jobProcess{control: plan.Control}
	}
	reject := func() (Process, error) {
		if owned != nil {
			return owned, ErrProvisioningRejected
		}
		return nil, ErrProvisioningRejected
	}
	if err != nil || owned == nil || plan.Bootstrap == nil || authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return reject()
	}
	wire := plan.Control.config.Wire
	identity := snapshot.Grant.Binding.Identity
	if wire.Generation != strconv.FormatUint(snapshot.Grant.Binding.Generation, 10) || wire.Identity.LaunchID != identity.LaunchID || wire.Identity.Epoch != identity.Epoch || wire.Identity.PolicyDigest != identity.PolicyDigest || wire.Binding.ArtifactSHA256 != hex.EncodeToString(plan.Spec.Artifact.SHA256[:]) || plan.Control.config.PollInterval > snapshot.Grant.BackendLeaseCeiling {
		return reject()
	}
	child, err := childjob.Start(ctx, plan.Spec)
	owned.child = child
	if err != nil || child == nil {
		return owned, ErrRuntimeUnconfirmed
	}
	// childを取得した後の失敗は必ず返し、Hostが失効後に同じJobを回収する。
	if authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return owned, ErrRuntimeUnconfirmed
	}
	err = completeBootstrap(plan.Bootstrap, ctx, snapshot.Grant.Binding)
	if err != nil || authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return owned, ErrRuntimeUnconfirmed
	}
	if err := plan.Control.Start(ctx); err != nil {
		return owned, ErrRuntimeUnconfirmed
	}
	if authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return owned, ErrRuntimeUnconfirmed
	}
	return owned, nil
}

func completeBootstrap(bootstrap func(context.Context, supervisor.Binding) error, ctx context.Context, binding supervisor.Binding) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return bootstrap(ctx, binding)
}

type jobProcess struct {
	child   *childjob.Child
	control *OwnedControl
}

func (p *jobProcess) Wait(ctx context.Context) error {
	if p == nil || p.control == nil || p.child == nil {
		return ErrRuntimeUnconfirmed
	}
	if err := p.control.Wait(ctx); err != nil {
		return ErrRuntimeUnconfirmed
	}
	return p.child.Wait(ctx)
}

func (p *jobProcess) Close(ctx context.Context) error {
	if p == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	if p.child == nil {
		return p.control.Abort(ctx)
	}
	deadline, _ := ctx.Deadline()
	// Goの応答待ちだけで全予算を消費せず、同じ期限内にJob回収の予算を残す。
	goBudget, cancel := context.WithTimeout(ctx, time.Until(deadline)/2)
	goErr := p.control.Stop(goBudget)
	cancel()
	if goErr != nil {
		_ = p.control.Abort(ctx)
	}
	jobErr := p.child.Close(ctx)
	if goErr != nil || jobErr != nil || ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	return nil
}
