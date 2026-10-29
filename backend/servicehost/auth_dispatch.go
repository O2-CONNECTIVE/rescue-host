package servicehost

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
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
	concrete  *operatorauth.Batch
	issuer    *ephemeral.Issuer
	owner     *serviceauthowner.Owner
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

func nativeServiceAuthBatch(batch *operatorauth.Batch, issuer *ephemeral.Issuer, owner *serviceauthowner.Owner) (serviceAuthBatchOps, error) {
	if !batch.MatchesOwner(owner, issuer) || batch.Collector() == nil {
		return serviceAuthBatchOps{}, ErrProvisioningUnconfigured
	}
	collector := batch.Collector()
	return serviceAuthBatchOps{concrete: batch, issuer: issuer, owner: owner, collector: collector, start: batch.Start, collect: batch.Collect, consume: collector.Consume, close: batch.Close}, nil
}

type serviceAuthRequest struct {
	adapter                                              *serviceAuthAdapter
	issuer                                               *ephemeral.Issuer
	owner                                                *serviceauthowner.Owner
	endpointExchange                                     bool // ownership path, never a readiness permit
	endpoint                                             *serviceAuthEndpoint
	ownerRunning, finalizing, batchConsumed, batchClosed bool // Service.mu
	batch                                                serviceAuthBatchOps
	request, worker                                      context.Context
	cancel                                               context.CancelFunc
	done                                                 chan struct{}
	ownerDone                                            chan struct{}
	cleanup                                              chan serviceCleanupRequest
	fatal                                                error
	finished, armed, cleaned                             bool // Service.mu
	digest                                               [32]byte
	ticket                                               *authenticatedReadTicket
	reply                                                *serviceDispatchReply
	closeGate                                            sync.Mutex
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
	if a == nil || a.service == nil || !authBudget(ctx) || !authBatchOperationsValid(batch) {
		return nil, ErrProvisioningRejected
	}
	s := a.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if !a.canReserveLocked(ctx) || !a.matchesBatchLocked(batch) {
		return nil, ErrProvisioningRejected
	}
	if _, seen := a.seen[batch.collector]; seen {
		return nil, ErrProvisioningRejected
	}
	if !authBudget(ctx) {
		return nil, ErrProvisioningRejected
	}
	r := a.reserveLocked(ctx)
	a.retainBatchLocked(r, batch)
	return r, nil
}

func (a *serviceAuthAdapter) previousAcknowledgedLocked() bool {
	if a.active == nil {
		return true
	}
	// Actual helper consumption, exact source/factory, physical write/close
	// receipts and final worker return are all required. The next worker still
	// requests a fresh SCM observation before acquiring its own Endpoint.
	if a.active.endpointExchange {
		held := a.active.endpoint
		if !a.active.authenticatedEndpointLocked() || held == nil || !held.cleaned || held.uncertain || !held.ops.concrete.MatchesCompletion(held.written, held.closed) {
			return false
		}
	}
	if !a.active.finished {
		return false
	}
	select {
	case <-a.active.ownerDone:
		return true
	default:
		return false
	}
}

func (a *serviceAuthAdapter) exchange(ctx context.Context, batch *operatorauth.Batch, write func(context.Context, []byte) error) error {
	if a == nil || a.service == nil {
		return ErrProvisioningRejected
	}
	a.service.mu.Lock()
	issuer, owner := a.service.issuer, a.service.authOwner
	a.service.mu.Unlock()
	ops, err := nativeServiceAuthBatch(batch, issuer, owner)
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
	return r.runOwner(func() error { return r.exchangeBody(write) })
}

func (r *serviceAuthRequest) exchangeBody(write func(context.Context, []byte) error) error {
	s := r.adapter.service
	s.mu.Lock()
	endpointPath := r.endpointExchange
	bound := r.endpointInputBoundLocked()
	s.mu.Unlock()
	if endpointPath && !bound {
		return ErrProvisioningUnconfigured
	}
	a, batch := r.adapter, r.batch
	if !endpointPath {
		if err := r.observeControl(r.worker, controlBeforeAcquire); err != nil {
			return err
		}
	}
	if err := batch.start(r.worker); err != nil {
		return authFailure(err, ErrProvisioningRejected)
	}
	a.service.mu.Lock()
	live := r.liveLocked()
	a.service.mu.Unlock()
	if !live {
		return ErrProvisioningRejected
	}
	read, err := batch.collect(r.worker)
	if err != nil || read == nil {
		return authFailure(err, ErrProvisioningRejected)
	}
	s = a.service
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
		return authFailure(err, ErrProvisioningRejected)
	}
	r.batchConsumed = true
	r.digest = sha256.Sum256(frame)
	r.ticket = &authenticatedReadTicket{marker: 1}
	r.armed = true
	s.mu.Unlock()
	if err := r.observeControl(r.worker, controlBeforeDispatch); err != nil {
		return err
	}
	reply, err := a.dispatch.begin(r.request, frame)
	if err != nil {
		return authFailure(err, ErrProvisioningRejected)
	}
	s.mu.Lock()
	r.reply = reply
	s.mu.Unlock()
	writeErr := r.writeDispatchReply(reply, write)
	if !authBudget(r.request) {
		writeErr = authFailure(writeErr, ErrRuntimeUnconfirmed)
	}
	if err = a.dispatch.complete(reply, writeErr); err != nil {
		return authFailure(errors.Join(writeErr, err), ErrRuntimeUnconfirmed)
	}
	return nil
}

func (r *serviceAuthRequest) liveLocked() bool {
	a, s := r.adapter, r.adapter.service
	return s.auth == a && a.active == r && s.dispatch == a.dispatch && s.issuer == r.issuer && s.authWriter.OpenLocked() && s.authWriter.MatchesLocked(r.batch.owner, r.issuer) && !s.stopping && s.state == Running && !r.finished && authBudget(r.worker)
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
	return r.closeOwned(ctx)
}

// Only the original exchange worker invokes Batch.Close. SCM may request a
// bounded retry but cannot move native owner cleanup onto its own thread.
func (r *serviceAuthRequest) awaitCleanup() error {
	for {
		request := <-r.cleanup
		if !authBudget(request.ctx) {
			request.reply <- ErrRuntimeUnconfirmed
			continue
		}
		err := r.close(request.ctx)
		request.reply <- err
		if err == nil {
			r.finish()
			return nil
		}
		if authFatal(err) {
			return err
		}
	}
}

func authFatal(err error) bool {
	return errors.Is(err, operatorauth.ErrFatal) || errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal)
}

// Preserve only known sentinels. Native error text must never reach responses,
// Service status or public diagnostics through this adapter.
func authFailure(err, kind error) error {
	if errors.Is(err, operatorauth.ErrFatal) {
		kind = errors.Join(kind, operatorauth.ErrFatal)
	}
	if errors.Is(err, bootstrapauth.ErrFatal) {
		kind = errors.Join(kind, bootstrapauth.ErrFatal)
	}
	if errors.Is(err, protectedkey.ErrFatal) {
		kind = errors.Join(kind, protectedkey.ErrFatal)
	}
	return kind
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
	s.mu.Unlock()
	request := serviceCleanupRequest{ctx: ctx, reply: make(chan error, 1)}
	select {
	case <-r.done:
	case <-r.ownerDone:
		s.mu.Lock()
		fatal := r.fatal
		cleaned := r.cleaned
		s.mu.Unlock()
		if fatal != nil {
			return fatal
		}
		if !cleaned {
			return ErrRuntimeUnconfirmed
		}
	case r.cleanup <- request:
		select {
		case err := <-request.reply:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ErrRuntimeUnconfirmed
		}
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	// Successful cleanup and worker return are separate acknowledgements.
	select {
	case <-r.done:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	select {
	case <-r.ownerDone:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	fatal := r.fatal
	s.mu.Unlock()
	if fatal != nil {
		return fatal
	}
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (s *Service) awaitAuthRequestBeforeReturn() {
	s.mu.Lock()
	var r *serviceAuthRequest
	if s.auth != nil {
		r = s.auth.active
	}
	s.mu.Unlock()
	if r != nil {
		<-r.ownerDone
		s.mu.Lock()
		fatal := r.fatal
		same := s.stopping && s.auth == r.adapter && r.adapter.service == s && s.auth.active == r && s.issuer == r.issuer
		s.mu.Unlock()
		// ownerDoneは実workerの終了。fatalで未発行のcleanup ackを
		// 成功へ変換せず保持し、native再試行なしでServiceを退役させる。
		if same && authFatal(fatal) {
			return
		}
		<-r.done
	}
}
