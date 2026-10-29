package servicehost

import (
	"context"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// Production keeps the concrete Witness; only package memory tests replace the
// operation seam. Its success is service observation, never authentication.
type serviceControlWitness struct {
	concrete                *serviceready.ServiceWitness
	confirm, recheck, close func(context.Context) error
}
type serviceControlReadiness struct {
	factories                   *serviceNativeFactories
	prepare                     func(context.Context) (serviceControlWitness, error)
	witness                     serviceControlWitness // SCM owner only
	prepared, closed, uncertain bool                  // SCM owner only
	requests                    chan serviceControlReadinessRequest
	calls                       map[*serviceAuthRequest]*[2]*serviceControlReadinessCall // Service.mu
}
type serviceControlPurpose uint8

const (
	controlBeforeAcquire serviceControlPurpose = iota
	controlBeforeDispatch
)

type serviceControlReadinessCall struct {
	answered    bool // Service.mu
	request     *serviceAuthRequest
	ctx         context.Context
	purpose     serviceControlPurpose
	observation *serviceControlObservation // Service.mu
	reply       chan serviceControlReadinessResult
	cancel      context.CancelFunc
}
type serviceControlReadinessRequest struct{ call *serviceControlReadinessCall }
type serviceControlReadinessResult struct {
	observation *serviceControlObservation
	err         error
}

// A private, single-use record of a fresh SCM query. It cannot authenticate an
// operator, prove SoleInput provenance, admit Start, or authorize next accept.
type serviceControlObservation struct {
	self    *serviceControlObservation
	control *serviceControlReadiness
	service *Service
	call    *serviceControlReadinessCall
	request *serviceAuthRequest
	issuer  *ephemeral.Issuer
	owner   *serviceauthowner.Owner
	purpose serviceControlPurpose
	used    bool // Service.mu
}

// This explicit path owns a query-only Witness. NewService and its original
// listener-inclusive Plane remain unchanged. No configured transport calls here.
func newControlledService(c ServiceConfig, f *serviceNativeFactories, p serviceDispatchProfile, limit uint64) (*Service, error) {
	if !f.canonical() || p.RequestLimit == 0 || p.RequestLimit >= limit || limit > 1024 {
		return nil, ErrProvisioningUnconfigured
	}
	c.Ready = serviceready.Config{}
	// Keep the real legacy constructor available; the explicit control branch
	// below uses Witness methods and never adapts them into a fake Plane.
	s, err := newService(c, func(ctx context.Context) (servicePlane, error) { return f.ready.Prepare(ctx) })
	if err != nil {
		return nil, err
	}
	if err := bindServiceControl(s, f, p, limit); err != nil {
		return nil, err
	}
	return s, nil
}
func bindServiceControl(s *Service, f *serviceNativeFactories, p serviceDispatchProfile, limit uint64) error {
	if s == nil || !f.canonical() || p.RequestLimit == 0 || p.RequestLimit >= limit || limit > 1024 {
		return ErrProvisioningUnconfigured
	}
	control := &serviceControlReadiness{factories: f, requests: make(chan serviceControlReadinessRequest, 1)}
	control.prepare = func(ctx context.Context) (serviceControlWitness, error) {
		w, err := f.prepareWitness(ctx)
		if w == nil {
			return serviceControlWitness{}, err
		}
		return serviceControlWitness{concrete: w, confirm: w.ConfirmServiceRunning, recheck: w.RecheckService, close: w.CloseService}, err
	}
	s.control = control
	s.callerGuard = f.ready.CheckServiceCaller
	if _, err := newServiceAuthAdapter(s, p, limit); err != nil {
		return err
	}
	return nil
}

func (c *serviceControlReadiness) prepareOnOwner(ctx context.Context) (err error) {
	if c.prepare == nil || c.prepared {
		return ErrProvisioningUnconfigured
	}
	c.uncertain = true
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	c.witness, err = c.prepare(ctx)
	c.uncertain = false
	c.prepared = true
	if err != nil {
		return authFailure(err, ErrProvisioningRejected)
	}
	if c.witness.confirm == nil || c.witness.recheck == nil || c.witness.close == nil {
		return ErrProvisioningUnconfigured
	}
	if !authBudget(ctx) {
		return ErrProvisioningRejected
	}
	return nil
}
func (c *serviceControlReadiness) confirmOnOwner(ctx context.Context) error {
	if !c.prepared || c.uncertain || c.witness.confirm == nil || !authBudget(ctx) {
		return ErrProvisioningRejected
	}
	return authObservedCall(ctx, c.witness.confirm)
}
func (c *serviceControlReadiness) recheckOnOwner(ctx context.Context) error {
	if !c.prepared || c.closed || c.uncertain || c.witness.recheck == nil || !authBudget(ctx) {
		return ErrProvisioningRejected
	}
	return authObservedCall(ctx, c.witness.recheck)
}
func (c *serviceControlReadiness) closeOnOwner(ctx context.Context) error {
	if c.closed {
		return nil
	}
	if c.witness.close != nil {
		if err := authObservedCall(ctx, c.witness.close); err != nil {
			return err
		}
	}
	if c.uncertain {
		return ErrRuntimeUnconfirmed
	}
	c.closed = true
	return nil
}
func authObservedCall(ctx context.Context, action func(context.Context) error) error {
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	err := authCleanupCall(func() error { return action(ctx) })
	if err != nil || !authBudget(ctx) {
		return authFailure(err, ErrRuntimeUnconfirmed)
	}
	return nil
}
func controlPurposeValid(p serviceControlPurpose) bool {
	return p == controlBeforeAcquire || p == controlBeforeDispatch
}
func (s *Service) controlCallLiveLocked(c *serviceControlReadiness, call *serviceControlReadinessCall) bool {
	if call == nil || call.request == nil || s.control != c || !controlPurposeValid(call.purpose) || !authBudget(call.ctx) {
		return false
	}
	r := call.request
	slots := c.calls[r]
	return slots != nil && slots[call.purpose] == call && r.adapter != nil && r.adapter.service == s && r.acquisitionLiveLocked(call.ctx)
}
func (r *serviceAuthRequest) requestControlObservation(ctx context.Context, purpose serviceControlPurpose) (*serviceControlObservation, error) {
	if r == nil || r.adapter == nil || !controlPurposeValid(purpose) || !authBudget(ctx) {
		return nil, ErrProvisioningRejected
	}
	s := r.adapter.service
	s.mu.Lock()
	c := s.control
	if c == nil || !r.acquisitionLiveLocked(ctx) {
		s.mu.Unlock()
		return nil, ErrProvisioningRejected
	}
	if c.calls == nil {
		c.calls = make(map[*serviceAuthRequest]*[2]*serviceControlReadinessCall)
	}
	slots := c.calls[r]
	if slots == nil {
		slots = new([2]*serviceControlReadinessCall)
		c.calls[r] = slots
	}
	if slots[purpose] != nil {
		s.mu.Unlock()
		return nil, ErrProvisioningRejected
	}
	// Never replace an earlier attempt, including one whose reply arrives late.
	deadline, _ := ctx.Deadline()
	workerDeadline, _ := r.worker.Deadline()
	if workerDeadline.Before(deadline) {
		deadline = workerDeadline
	}
	if protected := time.Now().Add(s.cfg.Budgets.Startup); protected.Before(deadline) {
		deadline = protected
	}
	bounded, cancel := context.WithDeadline(r.worker, deadline)
	stop := context.AfterFunc(ctx, cancel)
	release := func() { stop(); cancel() }
	call := &serviceControlReadinessCall{request: r, ctx: bounded, purpose: purpose, reply: make(chan serviceControlReadinessResult, 1), cancel: release}
	slots[purpose] = call
	s.mu.Unlock()
	select {
	case c.requests <- serviceControlReadinessRequest{call: call}:
	case <-bounded.Done():
		release()
		return nil, ErrProvisioningRejected
	case <-r.worker.Done():
		release()
		return nil, ErrProvisioningRejected
	case <-s.done:
		release()
		return nil, ErrProvisioningRejected
	}
	select {
	case result := <-call.reply:
		if result.err != nil {
			release()
			return nil, result.err
		}
		if result.observation == nil || !authBudget(bounded) || !authBudget(ctx) || !authBudget(r.worker) {
			release()
			return nil, ErrProvisioningRejected
		}
		return result.observation, nil
	case <-bounded.Done():
		release()
		return nil, ErrProvisioningRejected
	case <-r.worker.Done():
		release()
		return nil, ErrProvisioningRejected
	case <-s.done:
		release()
		return nil, ErrProvisioningRejected
	}
}

// handleControlReadiness runs only inside the SCM owner loop. Invalid/late calls
// issue nothing; actual Witness drift also terminates that Service owner.
func (s *Service) handleControlReadiness(request serviceControlReadinessRequest) error {
	call := request.call
	if call == nil {
		return nil
	}
	s.mu.Lock()
	c := s.control
	live := c != nil && !call.answered && s.controlCallLiveLocked(c, call)
	s.mu.Unlock()
	if !live {
		replyControlObservation(call, serviceControlReadinessResult{err: ErrProvisioningRejected})
		return nil
	}
	if err := c.recheckOnOwner(call.ctx); err != nil {
		replyControlObservation(call, serviceControlReadinessResult{err: err})
		return err
	}
	s.mu.Lock()
	if call.answered || !s.controlCallLiveLocked(c, call) {
		s.mu.Unlock()
		replyControlObservation(call, serviceControlReadinessResult{err: ErrProvisioningRejected})
		return nil
	}
	r := call.request
	observation := &serviceControlObservation{control: c, service: s, call: call, request: r, issuer: r.issuer, owner: r.owner, purpose: call.purpose}
	observation.self = observation
	call.answered = true
	call.observation = observation
	s.mu.Unlock()
	replyControlObservation(call, serviceControlReadinessResult{observation: observation})
	return nil
}
func (r *serviceAuthRequest) consumeControlObservation(observation *serviceControlObservation, purpose serviceControlPurpose) error {
	if r == nil || r.adapter == nil || observation == nil {
		return ErrProvisioningRejected
	}
	s := r.adapter.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if observation.self != observation || observation.service != s || observation.request != r || observation.purpose != purpose || observation.control != s.control || observation.issuer != r.issuer || observation.owner != r.owner || observation.used || observation.call == nil || observation.call.observation != observation {
		return ErrProvisioningRejected
	}
	observation.used = true
	defer observation.call.cancel()
	if !s.controlCallLiveLocked(s.control, observation.call) {
		return ErrProvisioningRejected
	}
	return nil
}
func (r *serviceAuthRequest) observeControl(ctx context.Context, purpose serviceControlPurpose) error {
	s := r.adapter.service
	s.mu.Lock()
	configured := s.control != nil
	s.mu.Unlock()
	if !configured {
		return nil
	} // Original Plane path retains its existing contract.
	observation, err := r.requestControlObservation(ctx, purpose)
	if err != nil {
		return err
	}
	return r.consumeControlObservation(observation, purpose)
}

func replyControlObservation(call *serviceControlReadinessCall, result serviceControlReadinessResult) {
	// A timed-out worker or a duplicate request cannot block the SCM owner.
	select {
	case call.reply <- result:
	default:
	}
}
