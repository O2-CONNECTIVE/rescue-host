package servicehost

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// This private fixed owner is registered before inspection. Native construction
// is absent. A package fake must model independent same-target nonadmin evidence;
// inspect success alone must never produce that evidence.
type confirmationTargetOps interface {
	inspect(context.Context) error
	commit(context.Context) error
	revoke() error // nonblocking admission latch only; never OS cleanup or a join
	close(context.Context) error
}
type confirmationTarget struct {
	ops       confirmationTargetOps
	preparing atomic.Bool
	inspected atomic.Bool
	revoked   atomic.Bool
}

type confirmationTargetPreparer interface {
	PrepareTarget(context.Context, PreviewConfirmationTicket) error
}

// Materialization is optional only for the existing already-held target seam.
// A runtime actor is fixed in the registry before this single preparation call.
func (r *previewConfirmation) prepareTarget(ctx context.Context) error {
	s := r.service
	s.mu.Lock()
	t := r.runtimeTarget
	if t == nil {
		s.mu.Unlock()
		return nil
	}
	prepare, ok := t.ops.(confirmationTargetPreparer)
	if !ok {
		s.mu.Unlock()
		return nil
	}
	registration := s.preview.targetOwner
	valid := authBudget(ctx) && r.liveLocked(time.Now()) && registration != nil && r.runtimeSource != nil && r.runtimeOwner != nil && registration.target == t && registration.source == r.runtimeSource && registration.owner == r.runtimeOwner && s.preview.runtime == registration.source && r.runtimeSource.target == t && r.runtimeOwner.target == t && !t.revoked.Load() && !t.inspected.Load() && t.preparing.CompareAndSwap(false, true)
	s.mu.Unlock()
	if !valid {
		return ErrProvisioningRejected
	}
	err := authCleanupCall(func() error { return prepare.PrepareTarget(ctx, PreviewConfirmationTicket{record: r}) })
	s.mu.Lock()
	live := r.liveLocked(time.Now()) && !t.revoked.Load()
	s.mu.Unlock()
	if err != nil || !live || !authBudget(ctx) {
		return authFailure(err, ErrRuntimeUnconfirmed)
	}
	return nil
}

type confirmationTargetLaunch struct {
	done   chan struct{}
	cancel context.CancelFunc
}

// The SCM owner keeps this registration even when no confirmation is accepted.
// Fields are frozen before any inspection or Acquire callback; mutable source
// reports never choose the cleanup or revocation target.
type confirmationTargetOwner struct {
	source      *confirmationRuntimeSource
	owner       *confirmationEvidenceOwner
	kernel      confirmationRuntimeKernel
	target      *confirmationTarget
	launch      *confirmationTargetLaunch // Service.mu
	closed      bool
	actorJoined bool
	fatal       error // original SCM cleanup thread only
}

func (s *Service) registerConfirmationTarget(source *confirmationRuntimeSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.preview == nil || s.preview.runtime != source || source == nil || source.self != source || source.target == nil || source.target.ops == nil || source.kernel == nil || s.preview.targetOwner != nil || s.preview.intent != nil || s.stopping || s.state != Running {
		return ErrProvisioningRejected
	}
	o := source.owner
	if o == nil || o.self != o || o.source != source || o.service != s || o.issuer != s.issuer || o.auth != s.authOwner || o.outer != s.outerTicket || o.target != source.target {
		return ErrProvisioningRejected
	}
	s.preview.targetOwner = &confirmationTargetOwner{source: source, owner: o, kernel: source.kernel, target: source.target}
	return nil
}

// preparedConfirmationTarget is the concrete childjob adapter. It retains the
// opaque same-target observation, not authentication or a runtime grant. No
// product source currently constructs this adapter or supplies its primary token.
type preparedConfirmationTarget struct {
	prepared             *childjob.Prepared
	observation          *childjob.Inspection
	expectation          *childjob.GuestTokenExpectation
	guestObservation     *childjob.GuestTokenObservation
	requireProtected     bool
	protectedObservation *childjob.ProtectedGuestObservation
}

func (t *preparedConfirmationTarget) inspect(ctx context.Context) error {
	if t.prepared == nil || t.expectation == nil {
		return ErrProvisioningUnconfigured
	}
	var err error
	t.observation, err = t.prepared.Inspect(ctx)
	if err == nil {
		t.guestObservation, err = t.prepared.ObserveGuestToken(ctx, t.observation, *t.expectation)
	}
	if err == nil && t.requireProtected {
		t.protectedObservation, err = t.prepared.ObserveProtectedGuest(ctx, t.observation)
	}
	return err
}
func (t *preparedConfirmationTarget) commit(ctx context.Context) error {
	if !t.prepared.GuestTokenCurrent(t.observation, t.guestObservation) {
		return ErrProvisioningRejected
	}
	if t.requireProtected && !t.prepared.ProtectedGuestCurrent(t.observation, t.protectedObservation) {
		return ErrProvisioningRejected
	}
	return t.prepared.CommitInspected(ctx, t.observation)
}
func (t *preparedConfirmationTarget) matchesGuestSID(sid string) bool {
	return t != nil && t.requireProtected && t.prepared != nil && t.prepared.GuestTokenCurrent(t.observation, t.guestObservation) && t.prepared.ProtectedGuestCurrent(t.observation, t.protectedObservation) && t.protectedObservation.MatchesGuestSID(sid)
}
func (t *preparedConfirmationTarget) revoke() error                   { return t.prepared.Revoke() }
func (t *preparedConfirmationTarget) close(ctx context.Context) error { return t.prepared.Close(ctx) }

func (t *confirmationTarget) revoke() error {
	if t == nil || t.ops == nil {
		return ErrRuntimeUnconfirmed
	}
	t.revoked.Store(true)
	return authCleanupCall(t.ops.revoke)
}
func (r *previewConfirmation) inspectTarget(ctx context.Context) error {
	source, o := r.runtimeSource, r.runtimeOwner
	if source == nil || (r.runtimeTarget == nil && source.target == nil && o != nil && o.target == nil) {
		return nil
	} // legacy memory seam; no product constructor
	t := r.runtimeTarget
	registration := r.service.preview.targetOwner
	if registration == nil || registration.source != source || registration.owner != o || registration.target != t || t == nil || source.target != t || r.service.preview.runtime != registration.source {
		return ErrProvisioningRejected
	}
	if o == nil || o.self != o || o.source != source || source.owner != o || o.target != t || t.ops == nil || t.revoked.Load() || t.inspected.Load() {
		return ErrProvisioningRejected
	}
	// The original actor already owns t, even if inspection or later Acquire fails.
	if err := authCleanupCall(func() error { return t.ops.inspect(ctx) }); err != nil {
		return err
	}
	if !authBudget(ctx) || t.revoked.Load() {
		return ErrRuntimeUnconfirmed
	}
	t.inspected.Store(true)
	return nil
}

// launchConfirmedTarget is an unconnected owner operation, not a dispatcher or
// product activation entry. It accepts only the retained exact actual challenge;
// callers cannot supply an approval bool, callback or reconstructed binding.
func (s *Service) launchConfirmedTarget(ctx context.Context, g *confirmedGrant) error {
	if s == nil || !authBudget(ctx) {
		return ErrProvisioningRejected
	}
	s.mu.Lock()
	if !s.confirmedTargetMatchesLocked(g) || g.launch != nil {
		s.mu.Unlock()
		return ErrProvisioningRejected
	}
	run, cancel := context.WithCancel(ctx)
	work := &confirmationTargetLaunch{done: make(chan struct{}), cancel: cancel}
	g.launch = work
	s.preview.targetOwner.launch = work // retained independently of mutable source/grant views
	// ownership published before issuer callback or OS side effects
	r, t := g.confirmation, g.target
	s.mu.Unlock()
	defer func() { cancel(); close(work.done) }()
	_, err := r.issuer.Launch(run, g.challenge, time.Now, func(call context.Context, a *supervisor.Authority) error {
		s.mu.Lock()
		live := s.confirmedTargetMatchesLocked(g)
		s.mu.Unlock()
		if !live || call.Err() != nil || a == nil || a.Snapshot(time.Now()).Grant.Binding != g.binding || a.Snapshot(time.Now()).State != supervisor.Granted {
			return ErrRuntimeUnconfirmed
		}
		return t.ops.commit(call)
	})
	if err != nil || !authBudget(run) || t.revoked.Load() {
		s.stopFor(ErrRuntimeUnconfirmed)
		return ErrRuntimeUnconfirmed
	}
	return nil // resume observation only; never product ready or cleanup acknowledgement
}
func (s *Service) confirmedTargetMatchesLocked(g *confirmedGrant) bool {
	if g == nil || g.target == nil || s.preview == nil || s.preview.grant != g || s.stopping || s.state != Running || s.issuer == nil || s.dispatch == nil {
		return false
	}
	r, t := g.confirmation, g.target
	registration := s.preview.targetOwner
	if registration == nil || registration.target != t || registration.source != s.preview.runtime {
		return false
	}
	if r == nil || !r.finished || r.service != s || r.issuer != s.issuer || r.owner != s.authOwner || r.grant != g || r.dispatcher != s.dispatch || s.dispatch.issuer != s.issuer || s.preview.intent != r.intent || r.intent == nil || r.intent.invalidated || r.intent.confirmation != r {
		return false
	}
	source, o, p := r.runtimeSource, r.runtimeOwner, g.proof
	if source == nil || source.self != source || source != s.preview.runtime || source.target != t || o == nil || o.self != o || o.target != t || source.owner != o || o.source != source || o.service != s || o.issuer != s.issuer || o.auth != s.authOwner || o.outer != s.outerTicket {
		return false
	}
	if p == nil || p != r.runtimeProof || p.self != p || p.target != t || p.source != source || p.owner != o || p.confirmation != r || p.service != s || p.issuer != s.issuer || p.auth != s.authOwner || p.outer != s.outerTicket || !p.consumed || p.stagingSID != o.expectedSID || p.runtimeSID != o.expectedSID || p.boundary != supervisor.SeparatedNonAdminGuest {
		return false
	}
	if !t.inspected.Load() || t.revoked.Load() || g.challenge.Binding() != g.binding || !sameGrantDescriptor(p.descriptor, s.preview.descriptor) || !time.Now().Before(s.preview.descriptor.HardDeadline) {
		return false
	}
	return s.reservation != nil && s.reservation == r.work.reservation && s.reservation.finished
}
func (s *Service) closeRegisteredTarget(ctx context.Context) error {
	s.mu.Lock()
	registration := s.preview.targetOwner
	var done <-chan struct{}
	if registration != nil && registration.launch != nil {
		done = registration.launch.done
	}
	s.mu.Unlock()
	if registration == nil {
		return nil
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ErrRuntimeUnconfirmed
		}
	}
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	if registration.closed {
		return nil
	}
	if err := authCleanupCall(func() error { return registration.target.ops.close(ctx) }); err != nil {
		return err
	}
	if !authBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	registration.closed = true
	return nil
}
func (s *Service) awaitTargetBeforeReturn() {
	s.mu.Lock()
	var done <-chan struct{}
	if s.preview != nil && s.preview.targetOwner != nil && s.preview.targetOwner.launch != nil {
		done = s.preview.targetOwner.launch.done
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	} // memory-only actual join, including fatal; never retry native cleanup
}
func (s *Service) reclaimRegisteredTarget(ctx context.Context, registration *confirmationTargetOwner) error {
	if registration.fatal != nil {
		return registration.fatal
	}
	// The frozen original actor owns the suspended target even without approval.
	err := s.closeRegisteredTarget(ctx)
	if err == nil && !registration.actorJoined {
		err = authCleanupCall(func() error { return registration.kernel.CloseOwned(ctx, registration.owner) })
		if err == nil && authBudget(ctx) {
			registration.actorJoined = true
		}
	}
	if err != nil {
		if authFatal(err) {
			registration.fatal = err
			s.mu.Lock()
			_ = s.authWriter.FatalWorkloadLocked(err)
			s.authWriter.NotifyPendingLocked()
			s.mu.Unlock()
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !authBudget(ctx) || !s.stopping || s.preview.targetOwner != registration || !s.workload.sameWorkloadLocked(s) || !s.workload.hostJoined {
		return ErrRuntimeUnconfirmed
	}
	if s.preview.intent != nil && s.preview.intent.confirmation != nil {
		r := s.preview.intent.confirmation
		if r.runtimeTarget == registration.target {
			r.runtimeCleanupConfirmed = true
		}
	}
	if err := s.authWriter.ConfirmWorkloadLocked(); err != nil {
		return ErrRuntimeUnconfirmed
	}
	s.authWriter.NotifyPendingLocked()
	return nil
}
