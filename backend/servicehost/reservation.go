package servicehost

import (
	"context"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// serviceOwnerIntentは所有順序のprivate ticketで、認証・policy・承認ではない。
// productionの作成口は未接続。configDigestや認証済み主体をここで生成しない。
type serviceOwnerIntent struct {
	owner   *Service
	issuer  *ephemeral.Issuer
	expires time.Time
	burned  bool
}
type serviceReservation struct {
	owner                       *Service
	issuer                      *ephemeral.Issuer
	intent                      *serviceOwnerIntent
	requestDone                 <-chan struct{}
	worker                      context.Context
	cancel                      context.CancelFunc
	done                        chan struct{}
	claimed, canceled, finished bool
	ticket                      *reservationWorkTicket
}

func (s *Service) reserveOwnerIntent(ctx context.Context, intent *serviceOwnerIntent) (*serviceReservation, error) {
	if s == nil || !controlBudget(ctx) {
		return nil, ErrProvisioningRejected
	}
	requestDone := ctx.Done()
	worker, cancel := context.WithCancel(context.Background())
	keep := false
	defer func() {
		if !keep {
			cancel()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authWriter.OpenLocked() || s.stopping || s.state != Running || s.dispatch == nil || s.issuer == nil || intent == nil || intent.owner != s || intent.issuer != s.issuer || s.ownerIntent != intent || intent.burned || s.reservation != nil {
		return nil, ErrProvisioningRejected
	}
	now := time.Now()
	select {
	case <-requestDone:
		return nil, ErrProvisioningRejected
	default:
	}
	if !now.Before(intent.expires) || !now.Round(0).Before(intent.expires.Round(0)) {
		return nil, ErrProvisioningRejected
	}
	intent.burned = true
	r := &serviceReservation{owner: s, issuer: s.issuer, intent: intent, requestDone: requestDone, worker: worker, cancel: cancel, done: make(chan struct{})}
	keep = true
	s.reservation = r
	return r, nil
}

type reservationWorkTicket struct{ marker byte }

// worker ticketは所有の単回受渡しだけ。grant/承認/実行許可ではない。
type serviceReservationWork struct {
	reservation *serviceReservation
	ticket      *reservationWorkTicket
}

func (w serviceReservationWork) Context() context.Context {
	if w.reservation == nil || w.ticket == nil {
		return nil
	}
	return w.reservation.worker
}
func (s *Service) consumeOwnerReservation(r *serviceReservation) (serviceReservationWork, error) {
	if s == nil {
		return serviceReservationWork{}, ErrProvisioningRejected
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r == nil || r.owner != s || s.reservation != r || r.issuer != s.issuer || !s.authWriter.OpenLocked() || s.stopping || r.canceled || r.claimed || r.finished {
		return serviceReservationWork{}, ErrProvisioningRejected
	}
	select {
	case <-r.requestDone:
		return serviceReservationWork{}, ErrProvisioningRejected
	default:
	}
	r.claimed = true
	r.ticket = &reservationWorkTicket{marker: 1}
	return serviceReservationWork{reservation: r, ticket: r.ticket}, nil
}

// workerの全所有処理が終了し、配送側が追加処理を開始しないと確認したownerだけが呼ぶ。
// 取消・ctx.Doneだけからこのackを作らない。wireのcleanup成功producerではない。
func (s *Service) finishOwnerReservation(work serviceReservationWork) error {
	if s == nil {
		return ErrProvisioningRejected
	}
	s.mu.Lock()
	r := work.reservation
	if r == nil || r.owner != s || s.reservation != r || r.issuer != s.issuer || !r.claimed || r.finished || work.ticket == nil || work.ticket != r.ticket {
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	r.finished = true
	close(r.done)
	cancel := r.cancel
	s.mu.Unlock()
	cancel()
	return nil
}

// 未claimの配送を同じlockで排除するか、既にclaimしたworkerの明示ackを待つ。
// 取消・期限・Host nilだけを完了へ変換しない。未確認でもrecordを保持する。
func (s *Service) waitOwnerReservation(ctx context.Context) error {
	if s == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	r := s.reservation
	if r == nil {
		s.mu.Unlock()
		return nil
	}
	if !s.stopping || r.owner != s || r.issuer != s.issuer {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	var cancel context.CancelFunc
	if !r.claimed && !r.finished {
		// stopの線形化後なので将来のconsumeも拒否される。配送を排除したowner abandon。
		r.canceled = true
		r.finished = true
		close(r.done)
		cancel = r.cancel
	}
	done := r.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	if ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reservation != r || !r.finished {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

// fatalなどでnative回収の再試行を止めた場合にも、未ack workerを残して
// ServeDoneを公開しない。ここではmemory上の配送排除と終了待機だけを行い、
// native処理・再認証・新たな実行を再開しない。ackがなければ所有参照を保持する。
func (s *Service) awaitOwnerReservationBeforeReturn() {
	s.mu.Lock()
	r := s.reservation
	if r == nil {
		s.mu.Unlock()
		return
	}
	if s.stopping && r.canceled && r.owner == s && r.issuer == s.issuer && !r.claimed && !r.finished {
		r.finished = true
		close(r.done)
	}
	done := r.done
	s.mu.Unlock()
	<-done
}
