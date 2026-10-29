package servicehost

import (
	"context"
	"errors"
	"time"
)

// OuterFinalizer は既存resource ownerへのcleanup-only RPCである。
// 呼出しthreadでnative再認証やtoken/root解放を行うcallbackには使わない。
type OuterFinalizer interface {
	Finalize(context.Context, InnerCleanupProof) error
}
type innerCleanupTicket struct{ marker byte }

// InnerCleanupProof は同じServiceのinner借用者が完全に終了したことだけを示す。
// wire値や自己申告boolから構築できず、operator認証や新しい許可を作らない。
type InnerCleanupProof struct {
	service *Service
	ticket  *innerCleanupTicket
}

func (p InnerCleanupProof) Confirms(service *Service) bool {
	if service == nil || p.service != service || p.ticket == nil {
		return false
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.confirmsInnerLocked(p)
}
func (s *Service) confirmsInnerLocked(proof InnerCleanupProof) bool {
	if proof.service != s || proof.ticket == nil || proof.ticket != s.outerTicket || !s.stopping || !s.innerCleanupConfirmed || s.done == nil {
		return false
	}
	if fatalServiceThread(errors.Join(s.stopCause, s.cleanupErr, s.serveErr, s.outerErr)) {
		return false
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// BindOuterFinalizer は一度だけ、Service利用前にouter ownerを結び付ける。
func (s *Service) BindOuterFinalizer(finalizer OuterFinalizer) error {
	if s == nil || finalizer == nil {
		return ErrProvisioningUnconfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil || s.prepare == nil && s.control == nil || s.cfg.Budgets.Startup <= 0 || s.cfg.Budgets.Cleanup <= 0 || s.cfg.Budgets.Observe <= 0 {
		return ErrProvisioningUnconfigured
	}
	if s.used || s.started || s.stopping || s.outer != nil {
		return ErrAlreadyUsed
	}
	s.outer = finalizer
	if s.preview != nil {
		s.preview.resolver, _ = finalizer.(PreviewConfirmationResolver)
	}
	s.outerConfirmed = false
	s.outerTicket = &innerCleanupTicket{marker: 1}
	if s.outerGate == nil {
		s.outerGate = make(chan struct{}, 1)
	}
	return nil
}
func (s *Service) refreshCleanupLocked() {
	s.cleanupConfirmed = s.innerCleanupConfirmed && (s.outer == nil || s.outerConfirmed)
	if s.cleanupConfirmed {
		s.state = Stopped
	} else {
		s.state = StopPending
	}
}
func callOuterFinalizer(ctx context.Context, finalizer OuterFinalizer, proof InnerCleanupProof) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	return finalizer.Finalize(ctx, proof)
}
func (s *Service) finalizeOuter(ctx context.Context) error {
	s.mu.Lock()
	if s.outer == nil {
		s.refreshCleanupLocked()
		confirmed, err := s.cleanupConfirmed, s.cleanupErr
		s.mu.Unlock()
		if !confirmed {
			return runtimeFailure(err)
		}
		return nil
	}
	gate := s.outerGate
	budget := s.cfg.Budgets.Cleanup
	s.mu.Unlock()
	if !controlBudget(ctx) || gate == nil || budget <= 0 {
		return ErrRuntimeUnconfirmed
	}
	cleanup, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	select {
	case gate <- struct{}{}:
	case <-cleanup.Done():
		return ErrRuntimeUnconfirmed
	}
	defer func() { <-gate }()
	s.mu.Lock()
	if s.outerConfirmed {
		s.mu.Unlock()
		return nil
	}
	proof := InnerCleanupProof{service: s, ticket: s.outerTicket}
	if !s.confirmsInnerLocked(proof) {
		err := runtimeFailure(s.stopCause, s.cleanupErr, s.serveErr, s.outerErr)
		s.mu.Unlock()
		return err
	}
	finalizer := s.outer
	s.mu.Unlock()
	err := callOuterFinalizer(cleanup, finalizer, proof)
	if cleanup.Err() != nil {
		err = runtimeFailure(err, cleanup.Err())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && !s.confirmsInnerLocked(proof) {
		err = runtimeFailure(s.stopCause, s.cleanupErr, s.serveErr, s.outerErr)
	}
	if fatalServiceThread(s.outerErr) {
		err = runtimeFailure(s.outerErr, err)
	}
	if err != nil {
		s.outerErr = runtimeFailure(err)
	} else {
		s.outerErr = nil
		s.outerConfirmed = true
	}
	s.refreshCleanupLocked()
	if err != nil {
		return runtimeFailure(err)
	}
	return nil
}

// waitServiceCleanupはdispatcher失敗時も同じownerのackを待ち、unknownをreturnしない。
// native非returnを解決せず、元owner以外へnative回収を移送しない。
func waitServiceCleanup(service *Service) error {
	interval := service.cfg.Budgets.Observe
	if interval <= 0 {
		return ErrProvisioningUnconfigured
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		cleanup, cancel := context.WithTimeout(context.Background(), service.cfg.Budgets.Cleanup)
		err := service.Close(cleanup)
		cancel()
		if service.Snapshot().CleanupConfirmed {
			return err
		}
		<-ticker.C
	}
}
func runReadySCMDispatch(service *Service, dispatch func() error) error {
	if service == nil || dispatch == nil {
		return ErrProvisioningUnconfigured
	}
	dispatchErr := dispatch()
	if cleanupErr := waitServiceCleanup(service); cleanupErr != nil {
		return runtimeFailure(cleanupErr)
	}
	if dispatchErr != nil {
		return ErrSCMUnavailable
	}
	service.mu.Lock()
	err := service.serveErr
	service.mu.Unlock()
	return err
}
