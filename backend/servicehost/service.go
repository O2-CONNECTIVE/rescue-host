package servicehost

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"runtime"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// ServiceConfig は保護配置元の値と認証済み承認sourceだけを受け取る。
// Provisioningはoperatorのserver認証を実装済みだと自己申告するboolではない。
// その実装/供給元は既定mainで未設定であり、UI/env/SCM引数を直接繋がない。
type ServiceConfig struct {
	Ready        serviceready.Config
	Provisioning ProtectedProvisioning
	Launcher     Launcher
	Budgets      Budgets
}
type servicePlane interface {
	Recheck(context.Context) error
	ConfirmRunning(context.Context) error
	Close(context.Context) error
}
type servicePrepare func(context.Context) (servicePlane, error)
type serviceThreadOperations struct{ lock, unlock func() }

type serviceCleanupRequest struct {
	ctx   context.Context
	reply chan error
}

// Service は一つのSCM起動のissuer/listener/launch回収を所有する。初回利用後はコピーしない。
type Service struct {
	mu                                        sync.Mutex
	cfg                                       ServiceConfig
	prepare                                   servicePrepare
	callerGuard                               func(context.Context) error
	approvalThread                            serviceThreadOperations
	serviceName                               string
	issuer                                    *ephemeral.Issuer
	dispatch                                  *serviceDispatcher
	auth                                      *serviceAuthAdapter
	ownerIntent                               *serviceOwnerIntent
	reservation                               *serviceReservation
	host                                      *Host
	delivery                                  *serviceHostDelivery
	workload                                  *serviceWorkloadReclaim
	plane                                     servicePlane
	cancel                                    context.CancelFunc
	used, started, stopping, cleanupConfirmed bool
	innerCleanupConfirmed, outerConfirmed     bool
	outer                                     OuterFinalizer
	outerTicket                               *innerCleanupTicket
	outerGate                                 chan struct{}
	outerErr                                  error
	state                                     ServiceState
	done                                      chan struct{}
	finish                                    sync.Once
	cleanupErr                                error
	serveErr                                  error
	stopCause                                 error
	retries                                   chan serviceCleanupRequest
	repeatStop                                chan struct{}
}
type ServiceSnapshot struct {
	State                                    ServiceState
	InstanceEpoch                            [32]byte
	Generation                               uint64
	HasAuthority, Stopping, CleanupConfirmed bool
}

func NewService(c ServiceConfig) (*Service, error) {
	if c.Provisioning == nil || c.Launcher == nil || c.Budgets.Startup <= 0 || c.Budgets.Cleanup <= 0 || c.Budgets.Observe <= 0 {
		return nil, ErrProvisioningUnconfigured
	}
	factory, err := serviceready.NewNative(c.Ready)
	if err != nil {
		return nil, ErrProvisioningRejected
	}
	name := c.Ready.Profile.ServiceName
	c.Ready = serviceready.Config{}
	service, err := newService(c, func(ctx context.Context) (servicePlane, error) { return factory.Prepare(ctx) })
	if err != nil {
		return nil, err
	}
	service.callerGuard = factory.CheckServiceCaller
	service.serviceName = name
	return service, nil
}

// 試験ではこのprivate OS factory seamだけを差し替える。公開constructorはnativeだけを選ぶ。
func newService(c ServiceConfig, prepare servicePrepare) (*Service, error) {
	if c.Provisioning == nil || c.Launcher == nil || c.Budgets.Startup <= 0 || c.Budgets.Cleanup <= 0 || c.Budgets.Observe <= 0 || prepare == nil {
		return nil, ErrProvisioningUnconfigured
	}
	return &Service{cfg: c, prepare: prepare, approvalThread: serviceThreadOperations{runtime.LockOSThread, runtime.UnlockOSThread}, done: make(chan struct{}), retries: make(chan serviceCleanupRequest), repeatStop: make(chan struct{}, 1), outerConfirmed: true, outerGate: make(chan struct{}, 1), state: StartPending}, nil
}
func (s *Service) Snapshot() ServiceSnapshot {
	if s == nil {
		return ServiceSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ServiceSnapshot{State: s.state, Stopping: s.stopping, CleanupConfirmed: s.cleanupConfirmed}
	if s.issuer != nil {
		snapshot := s.issuer.Snapshot()
		out.InstanceEpoch = snapshot.InstanceEpoch
		out.Generation = snapshot.Generation
		out.HasAuthority = snapshot.HasAuthority
	}
	return out
}

// Service authentication has its own fixed fatal sentinel. Preserve the
// classification without forwarding the native error's private text.
func serviceFailure(causes ...error) error {
	result := runtimeFailure(causes...)
	for _, cause := range causes {
		if errors.Is(cause, operatorauth.ErrFatal) {
			return errors.Join(result, operatorauth.ErrFatal)
		}
	}
	return result
}

func fatalServiceThread(err error) bool {
	return errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal) || errors.Is(err, operatorauth.ErrFatal)
}

// requestStopは新規発行拒否を先に公開し、pure issuerの全失効後にworkerを取り消す。
func (s *Service) requestStop() bool { return s.stopFor(nil) }
func (s *Service) stopFor(cause error) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	already := s.stopping
	if fatalServiceThread(cause) {
		s.stopCause = serviceFailure(s.stopCause, cause)
	} else if !already && cause != nil && !errors.Is(cause, context.Canceled) {
		s.stopCause = serviceFailure(cause)
	}
	s.stopping = true
	var unusedDone chan struct{}
	if !s.started && s.done != nil {
		s.used = true
		s.innerCleanupConfirmed = true
		s.refreshCleanupLocked()
		unusedDone = s.done
	} else if !s.cleanupConfirmed {
		s.state = StopPending
	}
	if s.ownerIntent != nil {
		s.ownerIntent.burned = true
	}
	var reservationCancel context.CancelFunc
	if r := s.reservation; r != nil && !r.finished {
		r.canceled = true
		reservationCancel = r.cancel
	}
	var deliveryCancel context.CancelFunc
	if d := s.delivery; d != nil {
		deliveryCancel = d.cancel
	}
	var authCancel context.CancelFunc
	if s.auth != nil && s.auth.active != nil && !s.auth.active.finished {
		authCancel = s.auth.active.cancel
	}
	i, cancel := s.issuer, s.cancel
	s.mu.Unlock()
	if unusedDone != nil {
		s.finish.Do(func() { close(unusedDone) })
	}
	if i != nil {
		_ = i.Close()
	}
	// 同issuerの失効より先にworker取消callbackを走らせない。
	if reservationCancel != nil {
		reservationCancel()
	}
	if deliveryCancel != nil {
		deliveryCancel()
	}
	if authCancel != nil {
		authCancel()
	}
	if cancel != nil {
		cancel()
	}
	return already
}
func (s *Service) setState(state ServiceState) { s.mu.Lock(); s.state = state; s.mu.Unlock() }
func publishServiceState[Status any](ctx context.Context, out chan<- Status, state Status) error {
	select {
	case out <- state:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Serveは元callerのOS threadでnative準備を行う。未確認のnative scopeをclean workerへ移さない。
func (s *Service) Serve(ctx context.Context, controls <-chan Control, states chan<- ServiceState) error {
	return serveService(ctx, s, controls, states, func(c Control) Control { return c }, func(state ServiceState) ServiceState { return state })
}

// SCM型も同じowner loopへ直接渡し、別のmessage bridge/無制限queueを作らない。
func serveService[Request, Status any](ctx context.Context, s *Service, controls <-chan Request, states chan<- Status, decode func(Request) Control, encode func(ServiceState) Status) (result error) {
	publish := func(ctx context.Context, state ServiceState) error {
		return publishServiceState(ctx, states, encode(state))
	}
	if s == nil || ctx == nil {
		return ErrProvisioningUnconfigured
	}
	s.mu.Lock()
	if s.done == nil || s.prepare == nil {
		s.mu.Unlock()
		return ErrProvisioningUnconfigured
	}
	if s.used {
		s.mu.Unlock()
		return ErrAlreadyUsed
	}
	s.used, s.started = true, true
	root, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	cfg := s.cfg
	stopping := s.stopping
	s.mu.Unlock()
	defer func() {
		s.awaitAuthRequestBeforeReturn()
		s.awaitOwnerReservationBeforeReturn()
		s.awaitHostDeliveryBeforeReturn()
		s.mu.Lock()
		s.serveErr = result
		s.mu.Unlock()
		s.finish.Do(func() { close(s.done) })
	}()
	defer cancel()
	if stopping {
		cancel()
	}
	runtime.LockOSThread()
	defer func() {
		if !fatalServiceThread(result) {
			runtime.UnlockOSThread()
		}
	}()
	issuer, err := ephemeral.New()
	if err != nil {
		s.mu.Lock()
		s.innerCleanupConfirmed = true
		s.refreshCleanupLocked()
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	s.mu.Lock()
	s.issuer = issuer
	s.workload = &serviceWorkloadReclaim{owner: s, issuer: issuer, changed: make(chan struct{})}
	stopping = s.stopping
	s.mu.Unlock()
	if stopping {
		_ = issuer.Close()
		cancel()
	}
	watchStop, watchDone := make(chan struct{}), make(chan struct{})
	interrogate := make(chan struct{}, 1)
	go func() {
		defer close(watchDone)
		for {
			select {
			case <-watchStop:
				return
			case request, open := <-controls:
				command := Stop
				if open {
					command = decode(request)
				} else {
					controls = nil
				}
				switch command {
				case Stop, Shutdown:
					if s.requestStop() {
						select {
						case s.repeatStop <- struct{}{}:
						default:
						}
					}
				case Interrogate:
					select {
					case interrogate <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	defer func() { close(watchStop); <-watchDone }()
	var plane servicePlane
	var approvalDone <-chan struct{}
	defer func() {
		s.stopFor(result)
		// 同じowner threadだけが再回収する。失敗してもSCM handlerをStoppedへ戻さない。
		attempt := func(parent context.Context) error {
			if parent == nil {
				parent = context.Background()
			}
			cleanup, end := context.WithTimeout(parent, cfg.Budgets.Cleanup)
			defer end()
			report, finish := context.WithTimeout(cleanup, min(cfg.Budgets.Observe, cfg.Budgets.Cleanup/8))
			_ = publish(report, StopPending)
			finish()
			// workload待機だけで全予算を使い、独立したauth owner回収を飢餓させない。
			deadline, _ := cleanup.Deadline()
			workload, endWorkload := context.WithTimeout(cleanup, time.Until(deadline)/2)
			closeErr := s.reclaimWorkload(workload)
			endWorkload()
			closeErr = errors.Join(closeErr, s.waitAuthRequest(cleanup))
			if closeErr == nil {
				if approvalDone != nil {
					select {
					case <-approvalDone:
					case <-cleanup.Done():
						closeErr = errors.Join(closeErr, ErrRuntimeUnconfirmed)
					}
				}
				if closeErr == nil && plane != nil {
					closeErr = errors.Join(closeErr, plane.Close(cleanup))
				}
				closeErr = errors.Join(closeErr, issuer.Close())
			}
			if cleanup.Err() != nil {
				closeErr = errors.Join(closeErr, ErrRuntimeUnconfirmed)
			}
			s.mu.Lock()
			s.cleanupErr = closeErr
			s.innerCleanupConfirmed = closeErr == nil
			s.refreshCleanupLocked()
			s.mu.Unlock()
			return closeErr
		}
		retryTimer := time.NewTicker(cfg.Budgets.Observe)
		defer retryTimer.Stop()
		closeErr := attempt(nil)
		for closeErr != nil && !fatalServiceThread(closeErr) && !fatalServiceThread(result) {
			select {
			case <-retryTimer.C:
				closeErr = attempt(nil)
			case retry := <-s.retries:
				if !controlBudget(retry.ctx) {
					retry.reply <- ErrRuntimeUnconfirmed
					continue
				}
				closeErr = attempt(retry.ctx)
				if closeErr != nil {
					retry.reply <- serviceFailure(closeErr)
				} else {
					retry.reply <- nil
				}
			case <-s.repeatStop:
				closeErr = attempt(nil)
			case <-interrogate:
				report, finish := context.WithTimeout(context.Background(), cfg.Budgets.Observe)
				_ = publish(report, StopPending)
				finish()
			}
		}
		if closeErr != nil {
			result = serviceFailure(result, closeErr)
		} else {
			s.mu.Lock()
			cause := s.stopCause
			s.mu.Unlock()
			if cause != nil {
				result = cause
			} else if root.Err() != nil && !fatalServiceThread(result) {
				result = nil
			}
		}
	}()

	startup, end := context.WithTimeout(root, cfg.Budgets.Startup)
	if err := publish(startup, StartPending); err != nil {
		end()
		return err
	}
	plane, err = s.prepare(startup)
	s.mu.Lock()
	s.plane = plane
	s.mu.Unlock()
	if err != nil || plane == nil || startup.Err() != nil {
		end()
		return serviceFailure(err)
	}
	if err = publish(startup, Running); err == nil {
		err = plane.ConfirmRunning(startup)
	}
	if err == nil {
		err = plane.Recheck(startup)
	}
	startupErr := startup.Err()
	end()
	if err != nil || startupErr != nil {
		return serviceFailure(err)
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return context.Canceled
	}
	s.state = Running
	s.mu.Unlock()
	type approved struct {
		policy ephemeral.ApprovedPolicy
		err    error
	}
	approvals := make(chan approved, 1)
	sourceDone := make(chan struct{})
	approvalDone = sourceDone
	thread := s.approvalThread
	s.mu.Lock()
	controlled := s.dispatch != nil
	s.mu.Unlock()
	if controlled {
		// controlled経路は許可0のstatus/quiesce専用。legacy承認をfallbackしない。
		approvals = nil
		close(sourceDone)
	} else {
		go func() {
			policy, err := runServiceApproval(root, cfg.Provisioning, thread)
			if fatalServiceThread(err) {
				s.stopFor(err)
			}
			approvals <- approved{policy, err}
			close(sourceDone)
		}()
	}
	observe := time.NewTicker(cfg.Budgets.Observe)
	defer observe.Stop()
	var hostDone <-chan error
	for {
		select {
		case <-root.Done():
			s.mu.Lock()
			cause := s.stopCause
			s.mu.Unlock()
			if cause != nil {
				return cause
			}
			return root.Err()
		case <-interrogate:
			report, finish := context.WithTimeout(root, cfg.Budgets.Observe)
			_ = publish(report, Running)
			finish()
		case <-observe.C:
			check, finish := context.WithTimeout(root, cfg.Budgets.Startup)
			err := plane.Recheck(check)
			finish()
			if err != nil {
				return serviceFailure(err)
			}
		case approval := <-approvals:
			approvals = nil
			if approval.err != nil {
				return serviceFailure(approval.err)
			}
			delivery, err := s.prepareLegacyHostDelivery(root, approval.policy)
			if err != nil {
				return err
			}
			hostDone = delivery.result
			go s.runHostDelivery(delivery)

		case err := <-hostDone:
			// 成否にかかわらず既存one-shot runnerの終了でservice起動を終える。自動再承認なし。
			return serviceFailure(err)
		}
	}
}

type lifetimePolicyValue struct{ policy ephemeral.ApprovedPolicy }

func (p lifetimePolicyValue) ReadApproved(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
	return p.policy, ctx.Err()
}
func runServiceApproval(ctx context.Context, source ProtectedProvisioning, thread serviceThreadOperations) (policy ephemeral.ApprovedPolicy, err error) {
	if thread.lock == nil || thread.unlock == nil {
		return policy, ErrProvisioningUnconfigured
	}
	thread.lock()
	defer func() {
		if !fatalServiceThread(err) {
			thread.unlock()
		}
	}()
	return readServiceApproval(ctx, source)
}
func readServiceApproval(ctx context.Context, source ProtectedProvisioning) (p ephemeral.ApprovedPolicy, err error) {
	defer func() {
		if recover() != nil {
			p = ephemeral.ApprovedPolicy{}
			err = ErrProvisioningRejected
		}
	}()
	if ctx.Err() != nil {
		return p, ctx.Err()
	}
	p, err = source.ReadApproved(ctx)
	if canceled := ctx.Err(); canceled != nil {
		if fatalServiceThread(err) {
			return ephemeral.ApprovedPolicy{}, errors.Join(canceled, serviceFailure(err))
		}
		return ephemeral.ApprovedPolicy{}, canceled
	}
	return p, err
}

// Closeの予算がなくても、発行拒否と全失効を先に行う。回収不明の成功扱いはしない。
func (s *Service) Close(ctx context.Context) (result error) {
	defer func() {
		if s == nil {
			return
		}
		s.mu.Lock()
		cause := s.stopCause
		s.mu.Unlock()
		if fatalServiceThread(cause) {
			result = serviceFailure(result, cause)
		}
	}()
	if s == nil {
		return ErrRuntimeUnconfirmed
	}
	s.requestStop()
	if !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return ErrRuntimeUnconfirmed
	}
	retry := serviceCleanupRequest{ctx: ctx, reply: make(chan error, 1)}
	select {
	case <-done:
	case s.retries <- retry:
		select {
		case err := <-retry.reply:
			if err != nil {
				return err
			}
		case <-done:
		case <-ctx.Done():
			return ErrRuntimeUnconfirmed
		}
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	err := s.cleanupErr
	s.mu.Unlock()
	if err != nil {
		return serviceFailure(err)
	}
	return s.finalizeOuter(ctx)
}
