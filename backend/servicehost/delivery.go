package servicehost

import (
	"context"
	"runtime"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// serviceHostDeliveryは同じServiceの実Host workerを登録してから公開する。
// worker joinはHost/process/Job/stdioの回収成功やoperator認証の証明ではない。
type serviceHostDelivery struct {
	owner             *Service
	issuer            *ephemeral.Issuer
	host              *Host
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	result            chan error
	started, finished bool
	outcome           error
	thread            serviceThreadOperations
}

// legacyの既存trusted typed sourceで得た値だけを扱う。controlled経路は拒否し、
// 所有intentを承認・policyへ昇格する口やlegacy fallbackを作らない。
func (s *Service) prepareLegacyHostDelivery(ctx context.Context, policy ephemeral.ApprovedPolicy) (*serviceHostDelivery, error) {
	if s == nil || ctx == nil {
		return nil, ErrProvisioningRejected
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancel(ctx)
	keep := false
	defer func() {
		if !keep {
			cancel()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || owned.Err() != nil {
		return nil, context.Canceled
	}
	if s.dispatch != nil || !s.started || s.state != Running || s.issuer == nil || s.host != nil || s.delivery != nil {
		return nil, ErrProvisioningRejected
	}
	host, err := WithRuntime(lifetimePolicyValue{policy}, s.cfg.Launcher, s.cfg.Budgets)
	if err != nil {
		return nil, ErrProvisioningRejected
	}
	host.borrowedIssuer = s.issuer
	host.serviceStop = func(cause error) { s.stopFor(cause) }
	d := &serviceHostDelivery{owner: s, issuer: s.issuer, host: host, ctx: owned, cancel: cancel, done: make(chan struct{}), result: make(chan error, 1), thread: serviceThreadOperations{runtime.LockOSThread, runtime.UnlockOSThread}}
	s.host, s.delivery = host, d
	keep = true
	return d, nil
}

func (s *Service) runHostDelivery(d *serviceHostDelivery) (result error) {
	if s == nil || d == nil {
		return ErrProvisioningRejected
	}
	s.mu.Lock()
	if d.owner != s || s.delivery != d || s.host != d.host || s.issuer != d.issuer || d.started || d.thread.lock == nil || d.thread.unlock == nil {
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	d.started = true
	s.mu.Unlock()
	d.thread.lock()
	result = d.host.Run(d.ctx)
	// 最終Run結果（defer中のfatalを含む）を停止原因へ反映してからjoinを公開する。
	s.stopFor(result)
	if !fatalServiceThread(result) {
		d.thread.unlock()
	}
	s.mu.Lock()
	d.outcome, d.finished = result, true
	s.mu.Unlock()
	d.result <- result
	close(d.done)
	return result
}

func (s *Service) waitHostDelivery(ctx context.Context) error {
	if !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	d := s.delivery
	s.mu.Unlock()
	if d == nil {
		return nil
	}
	select {
	case <-d.done:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	if ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivery != d || s.host != d.host || d.owner != s || d.issuer != s.issuer || !d.finished {
		return ErrRuntimeUnconfirmed
	}
	// join後もfatalはsticky。別threadでnative回収を再開したり、
	// process参照を捨ててinner cleanup proofへ変換しない。
	if fatalServiceThread(d.outcome) {
		return runtimeFailure(d.outcome)
	}
	return nil
}

// fatal回収が止まった経路も実workerの終了前にServeDoneを公開しない。
// ここでは参照を保持したmemory待機だけを行い、native処理は再開しない。
func (s *Service) awaitHostDeliveryBeforeReturn() {
	s.mu.Lock()
	d := s.delivery
	s.mu.Unlock()
	if d != nil {
		<-d.done
	}
}
