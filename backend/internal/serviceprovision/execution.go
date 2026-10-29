package serviceprovision

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// ExecutionConfigは固定保護sourceだけが供給し、argv/env/UIから構築しない。
// CleanupRetryは回収だけの再確認間隔で、認可・lease更新の周期ではない。
type ExecutionConfig struct {
	Anchor       Anchor
	CleanupRetry time.Duration
	Approval     servicehost.ProtectedProvisioning
}

// PreviewExecutionConfigは表示専用の実行所有を構成する固定値。
// 本人承認、強い起動token、guest/SSH起動の代用品ではない。
type PreviewExecutionConfig struct {
	Anchor       Anchor
	CleanupRetry time.Duration
	Limits       PreviewLimits
}

type executionStage interface {
	Initialize(context.Context) (*servicehost.Service, error)
	Close(context.Context) error
}
type executionCalls struct {
	guard  func(context.Context, Anchor) error
	stage  func() executionStage
	runSCM func(*servicehost.Service) error
}
type executionReady struct {
	service *servicehost.Service
	err     error
}
type executionRequest struct {
	ctx   context.Context
	proof servicehost.InnerCleanupProof
	reply chan error
}

// Executionは一つのservice起動の外側所有者であり、利用後にコピーしない。
// partial ownerは元OS threadに保持し、回収不明をmainのos.Exitへ落とさない。
type Execution struct {
	configured bool
	cfg        ExecutionConfig
	preview    *PreviewLimits // constructor-owned copy; nil selects the original legacy stage
	calls      executionCalls
	used       atomic.Bool
	service    atomic.Pointer[servicehost.Service]
	requests   chan executionRequest
	cleaned    chan struct{}
	stage      executionStage // native owner threadだけが操作する。失敗時も参照を保持する。
}

// NewNativeExecutionは値検査だけを行い、token/SCM/ファイルを開かない。
func NewNativeExecution(c ExecutionConfig) (*Execution, error) {
	if err := c.Anchor.Validate(); err != nil {
		return nil, err
	}
	if c.CleanupRetry <= 0 || c.Approval == nil {
		return nil, ErrApprovalUnconfigured
	}
	return newExecutionOwner(c), nil
}

// NewNativePreviewExecutionはlegacy承認を補完せず、表示専用経路を明示選択する。
// 入力不備なら元thread/root/SCMを取得する前に拒否する。
func NewNativePreviewExecution(c PreviewExecutionConfig) (*Execution, error) {
	if err := c.Anchor.Validate(); err != nil {
		return nil, err
	}
	if c.CleanupRetry <= 0 || c.Limits.RequestLimit == 0 ||
		c.Limits.RequestLimit >= c.Limits.LaunchLimit ||
		c.Limits.LaunchLimit > 1024 || c.Limits.IntentTTL <= 0 {
		return nil, ErrRootConfig
	}
	e := newExecutionOwner(ExecutionConfig{Anchor: c.Anchor, CleanupRetry: c.CleanupRetry})
	limits := c.Limits
	e.preview = &limits
	return e, nil
}

func newExecutionOwner(c ExecutionConfig) *Execution {
	return &Execution{configured: true, cfg: c, requests: make(chan executionRequest), cleaned: make(chan struct{})}
}

// Runはoriginal callerを拒否検査してから、固定scope専用の元OS threadを作る。
// SCM dispatcher自体はcallerで実行し、Executeのcleanup-only RPCを別ownerが処理する。
// 専用entry goroutineから使用する。caller guardのFatal後はそのgoroutineをreturnし、
// 同じthreadで他のprivileged処理を続けてはならない。
func (e *Execution) Run(ctx context.Context) error {
	if e == nil || !e.configured || ctx == nil {
		return ErrRootConfig
	}
	if !e.used.CompareAndSwap(false, true) {
		return servicehost.ErrAlreadyUsed
	}
	calls := e.calls
	if calls.guard == nil {
		calls.guard = executionCaller
	}
	if calls.stage == nil {
		calls.stage = func() executionStage { return &nativeExecutionStage{cfg: e.cfg, preview: e.preview} }
	}
	if calls.runSCM == nil {
		calls.runSCM = servicehost.RunReadySCM
	}
	runtime.LockOSThread()
	guardCtx, cancel := context.WithTimeout(ctx, e.cfg.Anchor.ReadTimeout)
	err := calls.guard(guardCtx, e.cfg.Anchor)
	guardErr := guardCtx.Err()
	cancel()
	if !errors.Is(err, ErrRootFatal) {
		runtime.UnlockOSThread()
	}
	if err != nil || guardErr != nil {
		return errors.Join(ErrRootDenied, err, guardErr)
	}
	ready := make(chan executionReady, 1)
	startup, startCancel := context.WithTimeout(ctx, e.cfg.Anchor.ReadTimeout)
	go e.own(startup, calls.stage, ready)
	state := <-ready
	startCancel()
	if state.err != nil {
		<-e.cleaned
		return state.err
	}
	// 一つのwatcherだけを持ち、Runのreturn前に必ずjoinする。
	stopWatch, watchDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-stopWatch:
			return
		case <-ctx.Done():
			e.closeService(state.service)
		}
	}()
	dispatchErr := calls.runSCM(state.service)
	cleanupErr := e.closeService(state.service)
	<-e.cleaned
	close(stopWatch)
	<-watchDone
	return errors.Join(dispatchErr, cleanupErr)
}

func executionCaller(ctx context.Context, a Anchor) error {
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	api := rootPlatform()
	if api == nil {
		return ErrRootUnavailable
	}
	p, err := api.principal(0)
	if err != nil || p.threadToken || p.kind != 1 || p.tokenID == 0 || !rootSID(p.user) {
		return errors.Join(ErrRootFatal, err)
	}
	sid, err := api.serviceSID(a.ServiceName)
	if err != nil || !rootServiceIdentifier(sid) || !rootHasServiceGroup(p, sid) || !rootLive(ctx) {
		return errors.Join(ErrRootDenied, err)
	}
	return nil
}

// Finalizeはopaqueなinner終端証拠を確認したcleanup-only RPC。
// callback callerでnative token/fileを閉じず、自己申告Snapshotも受け入れない。
func (e *Execution) Finalize(ctx context.Context, proof servicehost.InnerCleanupProof) error {
	if e == nil || !rootLive(ctx) || !proof.Confirms(e.service.Load()) {
		return ErrRootUnconfirmed
	}
	select {
	case <-e.cleaned:
		return nil
	default:
	}
	request := executionRequest{ctx: ctx, proof: proof, reply: make(chan error, 1)}
	select {
	case e.requests <- request:
	case <-e.cleaned:
		return nil
	case <-ctx.Done():
		return ErrRootUnconfirmed
	}
	select {
	case err := <-request.reply:
		return err
	case <-ctx.Done():
		return ErrRootUnconfirmed
	}
}
func (e *Execution) closeService(s *servicehost.Service) error {
	ticker := time.NewTicker(e.cfg.CleanupRetry)
	defer ticker.Stop()
	for {
		cleanup, cancel := context.WithTimeout(context.Background(), e.cfg.Anchor.CleanupTimeout)
		err := s.Close(cleanup)
		cancel()
		if s.Snapshot().CleanupConfirmed {
			return err
		}
		<-ticker.C
	}
}
