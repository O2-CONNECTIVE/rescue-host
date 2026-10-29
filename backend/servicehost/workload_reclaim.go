package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// serviceWorkloadReclaimは同じServiceのworkloadだけの記録。認証helper、
// listener、inner/outer回収proofを含まず、wireや外部factoryへ公開しない。
// 全fieldはowner.muで保護する。停止後に固定した所有参照は捨てない。
type serviceWorkloadReclaim struct {
	owner       *Service
	issuer      *ephemeral.Issuer
	reservation *serviceReservation
	delivery    *serviceHostDelivery
	host        *Host
	captured    bool
}

func (r *serviceWorkloadReclaim) sameOwnerLocked(s *Service, issuer *ephemeral.Issuer) bool {
	return r != nil && r.owner == s && s.workload == r && issuer != nil && r.issuer == issuer && s.issuer == issuer
}

func (r *serviceWorkloadReclaim) sameWorkloadLocked(s *Service) bool {
	return r.sameOwnerLocked(s, s.issuer) && s.stopping && r.captured &&
		r.reservation == s.reservation && r.delivery == s.delivery && r.host == s.host
}

// SCM ownerだけが呼ぶ。Host.Runのnative startup scopeは元workerで閉じる。
// ここでは既存の予約join→配送join/fatal検査→Host.Closeの順序を維持する。
func (s *Service) reclaimWorkload(ctx context.Context) error {
	if s == nil || !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	r := s.workload
	if !r.sameOwnerLocked(s, s.issuer) || !s.stopping || !r.issuer.Snapshot().Closed {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	if !r.captured {
		r.reservation, r.delivery, r.host = s.reservation, s.delivery, s.host
		r.captured = true
	}
	if !r.sameWorkloadLocked(s) {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	confirmed, fatal := s.authWriter.WorkloadLocked()
	if fatal != nil || confirmed {
		s.mu.Unlock()
		return fatal
	}
	host := r.host
	s.mu.Unlock()
	err := s.waitOwnerReservation(ctx)
	if err == nil {
		err = s.waitHostDelivery(ctx)
	}
	if err == nil {
		s.mu.Lock()
		valid := r.sameWorkloadLocked(s)
		s.mu.Unlock()
		if !valid {
			err = ErrRuntimeUnconfirmed
		} else if host != nil {
			err = host.Close(ctx)
		}
	}
	if !authBudget(ctx) {
		err = serviceFailure(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !r.sameWorkloadLocked(s) {
		err = serviceFailure(err)
	}
	if err == nil {
		if s.authWriter.ConfirmWorkloadLocked() != nil {
			err = ErrRuntimeUnconfirmed
		}
	} else {
		err = serviceFailure(err)
		if fatalServiceThread(err) {
			_ = s.authWriter.FatalWorkloadLocked(err)
		}
	}
	s.authWriter.NotifyPendingLocked()
	return err
}

// Compatibility fixture boundary. Production Batch awaits its concrete Owner
// directly. Validation of actual retained resources remains with SCM above.
func (s *Service) awaitWorkloadReclaim(ctx context.Context, issuer *ephemeral.Issuer) error {
	if s == nil {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	r := s.workload
	if !r.sameOwnerLocked(s, issuer) || (r.captured && !r.sameWorkloadLocked(s)) || !s.authWriter.MatchesLocked(s.authOwner, issuer) {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	owner := s.authOwner
	s.mu.Unlock()
	if err := owner.AwaitWorkload(ctx); err != nil {
		return serviceFailure(err)
	}
	return nil
}
