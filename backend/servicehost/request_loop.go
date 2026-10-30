package servicehost

import (
	"context"
	"time"
)

// RequestLoopBudgets are explicit protected-owner limits. Request bounds the
// whole attempt; Wait additionally bounds awaiting that attempt's connection.
type RequestLoopBudgets struct{ Wait, Request time.Duration }

func (b RequestLoopBudgets) Validate() error {
	if b.Wait <= 0 || b.Request <= 0 || b.Wait > time.Minute || b.Request > time.Minute || b.Wait > b.Request {
		return ErrProvisioningUnconfigured
	}
	return nil
}

type serviceRequestLoop struct {
	budgets  RequestLoopBudgets
	exchange func(context.Context) error // private operation seam; native production path below
	done     chan struct{}
	result   chan error
	started  bool // Service.mu
}

// NewRuntimeService selects serial authenticated request reception explicitly.
// It supplies no helper provider, authentication proof or execution authority.
func NewRuntimeService(c PreviewServiceConfig, requests RequestLoopBudgets) (*Service, error) {
	if err := requests.Validate(); err != nil {
		return nil, err
	}
	s, err := NewPreviewService(c)
	if err != nil {
		return nil, err
	}
	s.requestLoop = &serviceRequestLoop{budgets: requests, exchange: s.auth.exchangeControlEndpoint, done: make(chan struct{}), result: make(chan error, 1)}
	return s, nil
}

func (s *Service) startRequestLoop(parent context.Context) <-chan error {
	s.mu.Lock()
	loop := s.requestLoop
	if loop == nil {
		s.mu.Unlock()
		return nil
	}
	loop.started = true
	s.mu.Unlock()
	go func() {
		result := ErrRuntimeUnconfirmed
		defer func() {
			if recover() != nil {
				result = ErrRuntimeUnconfirmed
			}
			s.stopFor(result)
			loop.result <- result
			close(loop.done)
		}()
		for {
			request, cancel := context.WithTimeout(parent, loop.budgets.Request)
			s.mu.Lock()
			a := s.auth
			admitted := a != nil && a.canReserveLocked(request)
			var previous *serviceAuthRequest
			var attempts uint64
			if admitted {
				previous, attempts = a.active, a.attempts
			}
			s.mu.Unlock()
			if !admitted {
				cancel()
				result = ErrProvisioningRejected
				return
			}
			watchDone := make(chan struct{})
			stopWatch := context.AfterFunc(request, func() {
				s.stopFor(request.Err())
				close(watchDone)
			})
			result = runServiceRequest(request, loop.exchange)
			if !stopWatch() {
				<-watchDone
			}
			s.mu.Lock()
			complete := authBudget(request) && !s.stopping && s.auth == a && a.active != nil && a.active != previous && a.attempts == attempts+1 && a.previousAcknowledgedLocked()
			s.mu.Unlock()
			cancel()
			if result != nil {
				return
			}
			if !complete {
				result = ErrProvisioningRejected
				return
			}
		}
	}()
	return loop.result
}

// Each attempt gets its own worker. A fatal pinned worker is never reused for
// the next request, and returning a result is separate from its final join.
func runServiceRequest(ctx context.Context, exchange func(context.Context) error) error {
	result, done := make(chan error, 1), make(chan struct{})
	go func() {
		err := ErrRuntimeUnconfirmed
		defer func() {
			if recover() != nil {
				err = ErrRuntimeUnconfirmed
			}
			result <- err
			close(done)
		}()
		if !authBudget(ctx) {
			err = ErrProvisioningRejected
			return
		}
		err = exchange(ctx)
	}()
	err := <-result
	<-done
	return err
}

func (s *Service) waitRequestLoop(ctx context.Context) error {
	s.mu.Lock()
	loop := s.requestLoop
	started := loop != nil && loop.started
	s.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-loop.done:
		if authBudget(ctx) {
			return nil
		}
	case <-ctx.Done():
	}
	return ErrRuntimeUnconfirmed
}

func (s *Service) awaitRequestLoopBeforeReturn() {
	s.mu.Lock()
	loop := s.requestLoop
	started := loop != nil && loop.started
	s.mu.Unlock()
	if started {
		<-loop.done
	}
}
