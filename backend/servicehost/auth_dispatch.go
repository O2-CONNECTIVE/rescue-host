package servicehost

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// Private composition only. No configured native producer calls this adapter
// yet; neither wire values nor a boolean can construct an authenticated Read.
type serviceAuthAdapter struct {
	service         *Service
	dispatch        *serviceDispatcher
	limit, attempts uint64 // Service.mu
	active          *serviceAuthRequest
	seen            map[*operatorauth.Collector]struct{}
}

// Production operations always wrap a concrete prepared Batch and its exact
// Collector. Only package tests replace this private operation seam.
type serviceAuthBatchOps struct {
	collector *operatorauth.Collector
	start     func(context.Context) error
	collect   func(context.Context) (*operatorauth.Read, error)
	consume   func(context.Context, *operatorauth.Read) ([]byte, error)
	close     func(context.Context) error
}

// Check the actual deadline as well as Err: timer delivery may be delayed.
func authBudget(ctx context.Context) bool {
	if !controlBudget(ctx) {
		return false
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Now().Before(deadline)
}

func nativeServiceAuthBatch(batch *operatorauth.Batch) (serviceAuthBatchOps, error) {
	if batch == nil || batch.Collector() == nil {
		return serviceAuthBatchOps{}, ErrProvisioningUnconfigured
	}
	collector := batch.Collector()
	return serviceAuthBatchOps{collector: collector, start: batch.Start, collect: batch.Collect, consume: collector.Consume, close: batch.Close}, nil
}

type serviceAuthRequest struct {
	adapter                           *serviceAuthAdapter
	issuer                            *ephemeral.Issuer
	batch                             serviceAuthBatchOps
	request, worker                   context.Context
	cancel                            context.CancelFunc
	done                              chan struct{}
	running, finished, armed, cleaned bool // Service.mu
	digest                            [32]byte
	ticket                            *authenticatedReadTicket
	closeGate                         sync.Mutex
}

func newServiceAuthAdapter(s *Service, profile serviceDispatchProfile, launchLimit uint64) (*serviceAuthAdapter, error) {
	if s == nil || launchLimit == 0 || launchLimit > 1024 {
		return nil, ErrProvisioningUnconfigured
	}
	a := &serviceAuthAdapter{service: s, limit: launchLimit, seen: make(map[*operatorauth.Collector]struct{})}
	d, err := newServiceDispatcher(s, profile, a)
	if err != nil {
		return nil, err
	}
	a.dispatch = d
	s.mu.Lock()
	s.auth = a
	s.mu.Unlock()
	return a, nil
}

// A non-nil request is the ownership handoff. Rejection leaves the prepared
// Batch with its caller, which must retain/close it; it has never been started.
func (a *serviceAuthAdapter) register(ctx context.Context, batch serviceAuthBatchOps) (*serviceAuthRequest, error) {
	if a == nil || a.service == nil || !authBudget(ctx) || batch.collector == nil || batch.start == nil || batch.collect == nil || batch.consume == nil || batch.close == nil {
		return nil, ErrProvisioningRejected
	}
	s := a.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auth != a || s.dispatch != a.dispatch || s.state != Running || s.stopping || s.issuer == nil || a.attempts >= a.limit || (a.active != nil && !a.active.finished) {
		return nil, ErrProvisioningRejected
	}
	if _, seen := a.seen[batch.collector]; seen {
		return nil, ErrProvisioningRejected
	}
	if !authBudget(ctx) {
		return nil, ErrProvisioningRejected
	}
	worker, cancel := context.WithCancel(ctx)
	r := &serviceAuthRequest{adapter: a, issuer: s.issuer, batch: batch, request: ctx, worker: worker, cancel: cancel, done: make(chan struct{}), running: true}
	a.attempts++
	a.seen[batch.collector] = struct{}{}
	a.active = r
	return r, nil
}

func (a *serviceAuthAdapter) exchange(ctx context.Context, batch *operatorauth.Batch, write func(context.Context, []byte) error) error {
	ops, err := nativeServiceAuthBatch(batch)
	if err != nil {
		return err
	}
	return a.exchangeOps(ctx, ops, write)
}

func (a *serviceAuthAdapter) exchangeOps(ctx context.Context, batch serviceAuthBatchOps, write func(context.Context, []byte) error) (result error) {
	if write == nil {
		return ErrProvisioningRejected
	}
	r, err := a.register(ctx, batch)
	if err != nil {
		return err
	}
	defer func() {
		if recover() != nil {
			result = ErrRuntimeUnconfirmed
		}
		if result != nil {
			a.service.stopFor(result)
		}
		confirmed := result == nil
		if result != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), a.service.cfg.Budgets.Cleanup)
			err := r.close(cleanup)
			cancel()
			confirmed = err == nil
			if err != nil {
				result = ErrRuntimeUnconfirmed
			}
		}
		a.service.mu.Lock()
		r.running = false
		r.cleaned = r.cleaned || confirmed
		a.service.mu.Unlock()
		r.cancel()
		if confirmed {
			r.finish()
		}
	}()
	if batch.start(r.worker) != nil {
		return ErrProvisioningRejected
	}
	a.service.mu.Lock()
	live := r.liveLocked()
	a.service.mu.Unlock()
	if !live {
		return ErrProvisioningRejected
	}
	read, err := batch.collect(r.worker)
	if err != nil || read == nil {
		return ErrProvisioningRejected
	}
	s := a.service
	s.mu.Lock()
	if !r.liveLocked() {
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	s.mu.Unlock()
	frame, err := batch.consume(r.worker, read)
	s.mu.Lock()
	if err != nil || !r.liveLocked() {
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	r.digest = sha256.Sum256(frame)
	r.ticket = &authenticatedReadTicket{marker: 1}
	r.armed = true
	s.mu.Unlock()
	reply, err := a.dispatch.begin(r.request, frame)
	if err != nil {
		return ErrProvisioningRejected
	}
	writeErr := write(r.request, append([]byte(nil), reply.frame...))
	if !authBudget(r.request) {
		writeErr = ErrRuntimeUnconfirmed
	}
	if err = a.dispatch.complete(reply, writeErr); err != nil {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (r *serviceAuthRequest) liveLocked() bool {
	a, s := r.adapter, r.adapter.service
	return s.auth == a && a.active == r && s.dispatch == a.dispatch && s.issuer == r.issuer && !s.stopping && s.state == Running && !r.finished && authBudget(r.worker)
}

func (a *serviceAuthAdapter) verify(ctx context.Context, frame []byte) (authenticatedServiceRead, error) {
	if a == nil || a.service == nil {
		return authenticatedServiceRead{}, ErrProvisioningRejected
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	r := a.active
	if r == nil || !r.liveLocked() || !r.armed || !authBudget(ctx) || r.digest != sha256.Sum256(frame) {
		return authenticatedServiceRead{}, ErrProvisioningRejected
	}
	r.armed = false
	return authenticatedServiceRead{owner: a.dispatch, frame: r.digest, ticket: r.ticket}, nil
}

func (r *serviceAuthRequest) finish() {
	s := r.adapter.service
	s.mu.Lock()
	if !r.finished {
		r.finished = true
		r.armed = false
		close(r.done)
	}
	s.mu.Unlock()
}

func (r *serviceAuthRequest) close(ctx context.Context) (result error) {
	if !authBudget(ctx) || !r.closeGate.TryLock() {
		return ErrRuntimeUnconfirmed
	}
	defer r.closeGate.Unlock()
	defer func() {
		if recover() != nil {
			result = ErrRuntimeUnconfirmed
		}
	}()
	r.adapter.service.mu.Lock()
	cleaned := r.cleaned
	r.adapter.service.mu.Unlock()
	if cleaned {
		return nil
	}
	if r.batch.close(ctx) != nil || !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	r.adapter.service.mu.Lock()
	r.cleaned = true
	r.adapter.service.mu.Unlock()
	return nil
}

func (s *Service) waitAuthRequest(ctx context.Context) error {
	if s == nil || !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	if s.auth == nil || s.auth.active == nil {
		s.mu.Unlock()
		return nil
	}
	r := s.auth.active
	if r.adapter.service != s || r.adapter != s.auth || r.issuer != s.issuer || !s.stopping {
		s.mu.Unlock()
		return ErrRuntimeUnconfirmed
	}
	running, finished := r.running, r.finished
	s.mu.Unlock()
	if finished {
		return nil
	}
	if !running {
		if err := r.close(ctx); err != nil {
			return err
		}
		r.finish()
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (s *Service) awaitAuthRequestBeforeReturn() {
	s.mu.Lock()
	var done <-chan struct{}
	if s.auth != nil && s.auth.active != nil {
		done = s.auth.active.done
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
