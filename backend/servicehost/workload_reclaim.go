package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

type workloadReclaimState uint8

const (
	workloadReclaimPending workloadReclaimState = iota
	workloadReclaimConfirmed
	workloadReclaimFatal
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
	state       workloadReclaimState
	fatal       error
	changed     chan struct{}
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
	if r.state == workloadReclaimFatal {
		err := r.fatal
		s.mu.Unlock()
		return err
	}
	if r.state == workloadReclaimConfirmed {
		s.mu.Unlock()
		return nil
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
		r.state = workloadReclaimConfirmed
	} else {
		err = serviceFailure(err)
		if fatalServiceThread(err) {
			r.state, r.fatal = workloadReclaimFatal, err
		}
	}
	close(r.changed)
	r.changed = make(chan struct{})
	return err
}

// 将来のnative batchのcallbackはこの有限memory待機だけを使う。
// Service.Close、SCM再試行、auth join、plane.Closeをここから呼ばない。
// Host nilや取消だけで完了せず、同じ所有物のjoin/回収記録を待つ。
func (s *Service) awaitWorkloadReclaim(ctx context.Context, issuer *ephemeral.Issuer) error {
	if s == nil {
		return ErrRuntimeUnconfirmed
	}
	for authBudget(ctx) {
		s.mu.Lock()
		r := s.workload
		if !r.sameOwnerLocked(s, issuer) {
			s.mu.Unlock()
			return ErrRuntimeUnconfirmed
		}
		if r.captured && !r.sameWorkloadLocked(s) {
			s.mu.Unlock()
			return ErrRuntimeUnconfirmed
		}
		state, fatal, changed := r.state, r.fatal, r.changed
		s.mu.Unlock()
		if state == workloadReclaimConfirmed {
			return nil
		}
		if state == workloadReclaimFatal {
			return fatal
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ErrRuntimeUnconfirmed
		}
	}
	return ErrRuntimeUnconfirmed
}
