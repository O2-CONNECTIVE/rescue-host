package servicehost

import (
	"context"
	"encoding/hex"
	"errors"
	"runtime"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// ChildPlanは未決の保護bootstrapが準備する値。任意path/tokenや新wireを採用しない。
// Bootstrapは同じ起動へ型付きConfigを安全に渡す未実装依存。
// 借用child-side pipe/tokenはprovisionerが所有し、startup完了/取消時に不要な複製を閉じる。
// 識別子やhashだけを相手認証へ読み替えてはならない。
type ChildPlan struct {
	Spec              childjob.Spec
	Control           *OwnedControl
	Stdio             *OwnedStdio
	Bootstrap         func(context.Context, supervisor.Binding) error
	PreparedBootstrap PreparedBootstrap
}

// PreparedBootstrap は同じAuthorityを受け取る、一回送信と所有回収の境界です。
// native実装は認証済みserviceでのみ構成し、識別子だけを認証と扱いません。
type PreparedBootstrap interface {
	Publish(context.Context, *supervisor.Authority) error
	Close(context.Context) error
}

type issuedChildProvisioning interface {
	prepareIssued(context.Context, *supervisor.Authority, IssuedLaunch) (ChildPlan, error)
}

type ProtectedChildProvisioning interface {
	PrepareChild(context.Context, supervisor.ProvisionedGrant) (ChildPlan, error)
}

type JobRuntime struct {
	source     ProtectedChildProvisioning
	startChild func(context.Context, childjob.Spec) (Process, error)
}

// NewJobRuntimeはadapterを構成するだけで、OSの設定・token取得・process起動はしない。
// default SCM/mainはsourceを設定せず、実機起動には別のaction-time承認を要する。
func NewJobRuntime(source ProtectedChildProvisioning) *JobRuntime {
	return &JobRuntime{source: source, startChild: func(ctx context.Context, spec childjob.Spec) (Process, error) { return childjob.Start(ctx, spec) }}
}

func (r *JobRuntime) Start(ctx context.Context, authority *supervisor.Authority) (Process, error) {
	return r.start(ctx, authority, nil)
}

// StartIssued はcallerの元threadを固定し、native検査中の別threadへの移送を行いません。
func (r *JobRuntime) StartIssued(ctx context.Context, a *supervisor.Authority, issued IssuedLaunch) (p Process, err error) {
	runtime.LockOSThread()
	defer func() {
		if !errors.Is(err, bootstrapauth.ErrFatal) {
			runtime.UnlockOSThread()
		}
	}()
	return r.start(ctx, a, &issued)
}

func (r *JobRuntime) start(ctx context.Context, authority *supervisor.Authority, issued *IssuedLaunch) (Process, error) {
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
	var plan ChildPlan
	var err error
	if source, ok := r.source.(issuedChildProvisioning); ok {
		if issued == nil || issued.instance == ([32]byte{}) || issued.binding != snapshot.Grant.Binding {
			return nil, ErrProvisioningRejected
		}
		plan, err = source.prepareIssued(ctx, authority, *issued)
	} else {
		plan, err = r.source.PrepareChild(ctx, snapshot.Grant)
	}
	var owned *jobProcess
	if plan.Control != nil || plan.PreparedBootstrap != nil || plan.Stdio != nil {
		owned = &jobProcess{control: plan.Control, stdio: plan.Stdio, bootstrap: plan.PreparedBootstrap, authority: authority, binding: snapshot.Grant.Binding}
		if plan.Stdio != nil {
			plan.Stdio.attach(authority, snapshot.Grant.Binding)
		}
	}
	reject := func() (Process, error) {
		if owned != nil {
			return owned, errors.Join(ErrProvisioningRejected, err)
		}
		return nil, errors.Join(ErrProvisioningRejected, err)
	}
	if err != nil || owned == nil || plan.Control == nil || (plan.Bootstrap == nil) == (plan.PreparedBootstrap == nil) || authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return reject()
	}
	wire := plan.Control.config.Wire
	identity := snapshot.Grant.Binding.Identity
	if wire.Generation != strconv.FormatUint(snapshot.Grant.Binding.Generation, 10) || wire.Identity.LaunchID != identity.LaunchID || wire.Identity.Epoch != identity.Epoch || wire.Identity.PolicyDigest != identity.PolicyDigest || wire.Binding.ArtifactSHA256 != hex.EncodeToString(plan.Spec.Artifact.SHA256[:]) || plan.Control.config.PollInterval > snapshot.Grant.BackendLeaseCeiling {
		return reject()
	}
	if b := plan.Spec.Bootstrap; b != nil {
		if issued == nil || b.Values.IssuerInstanceEpoch != hex.EncodeToString(issued.instance[:]) || b.Values.Wire != wire {
			return reject()
		}
	}
	if r.startChild == nil {
		return reject()
	}
	child, err := r.startChild(ctx, plan.Spec)
	if child != nil {
		owned.child = child
	}
	if err != nil || child == nil {
		return owned, ErrRuntimeUnconfirmed
	}
	// childを取得した後の失敗は必ず返し、Hostが失効後に同じJobを回収する。
	if authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return owned, ErrRuntimeUnconfirmed
	}
	if plan.PreparedBootstrap != nil {
		err = publishBootstrap(plan.PreparedBootstrap, ctx, authority)
	} else {
		err = completeBootstrap(plan.Bootstrap, ctx, snapshot.Grant.Binding)
	}
	if err != nil || authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return owned, errors.Join(ErrRuntimeUnconfirmed, err)
	}
	if err := plan.Control.Start(ctx); err != nil {
		return owned, ErrRuntimeUnconfirmed
	}
	if authority.Snapshot(time.Now()).State != supervisor.Granted || ctx.Err() != nil {
		return owned, ErrRuntimeUnconfirmed
	}
	return owned, nil
}

func publishBootstrap(b PreparedBootstrap, ctx context.Context, a *supervisor.Authority) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return b.Publish(ctx, a)
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
	child     Process
	control   *OwnedControl
	bootstrap PreparedBootstrap
	stdio     *OwnedStdio
	authority *supervisor.Authority
	binding   supervisor.Binding
}

// OwnProcessは既に保護側が所有したOS processと同じ子の制御pipeを組み合わせる。
// process/pipeを新規作成せず、認可・相手認証・Go準備完了を自己申告しない。
func OwnProcess(child Process, control *OwnedControl) (Process, error) {
	if child == nil || control == nil {
		return nil, ErrProvisioningRejected
	}
	return &jobProcess{child: child, control: control}, nil
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

func (p *jobProcess) Close(ctx context.Context) (result error) {
	if p == nil {
		return ErrRuntimeUnconfirmed
	}
	if p.authority != nil {
		_ = p.authority.Revoke(p.binding)
	}
	if !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	// stderrやGo停止の失敗でも、独立して所有するbootstrap/stdio回収を飛ばさない。
	defer func() {
		var err error
		if p.bootstrap != nil {
			err = p.bootstrap.Close(ctx)
		}
		if p.stdio != nil {
			err = errors.Join(err, p.stdio.Close(ctx))
		}
		if err != nil {
			result = runtimeFailure(result, err)
		}
	}()
	if p.child == nil {
		if p.control != nil {
			return p.control.Abort(ctx)
		}
		return nil
	}
	if p.control == nil {
		_ = p.child.Close(ctx)
		return ErrRuntimeUnconfirmed
	}
	deadline, _ := ctx.Deadline()
	// Go停止とstdout実EOFには残りの半分だけを使い、Job回収を塞がない。
	goBudget, cancel := context.WithTimeout(ctx, time.Until(deadline)/2)
	goErr := p.control.Stop(goBudget)
	cancel()
	if goErr == nil {
		// exit0/Job空を別に確認し、さらにstderr実EOFとdrain joinを待つ。
		graceful, end := context.WithTimeout(ctx, time.Until(deadline)/2)
		waitErr := p.child.Wait(graceful)
		end()
		if waitErr == nil && ctx.Err() == nil {
			if p.stdio != nil {
				// stderrがEOFを返さなくても、取消/資源回収の残り時間を確保する。
				stderrBudget, finish := context.WithTimeout(ctx, time.Until(deadline)/2)
				stderrErr := p.stdio.Wait(stderrBudget)
				finish()
				if stderrErr != nil {
					return ErrRuntimeUnconfirmed
				}
			}
			return nil
		}
	}
	if goErr != nil {
		_ = p.control.Abort(ctx)
	}
	_ = p.child.Close(ctx)
	// 強制回収はGo Close/exit0の証拠に変換しない。
	return ErrRuntimeUnconfirmed
}
