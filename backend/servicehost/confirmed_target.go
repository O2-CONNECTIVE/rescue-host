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
	inspected atomic.Bool
	revoked   atomic.Bool
}
type confirmationTargetLaunch struct {
	done   chan struct{}
	cancel context.CancelFunc
}

// preparedConfirmationTarget is the concrete childjob adapter. It retains the
// opaque same-target observation, not authentication or a runtime grant. No
// product source currently constructs this adapter or supplies its primary token.
type preparedConfirmationTarget struct {
	prepared    *childjob.Prepared
	observation *childjob.Inspection
}

func (t *preparedConfirmationTarget) inspect(ctx context.Context) error {
	var err error
	t.observation, err = t.prepared.Inspect(ctx)
	return err
}
func (t *preparedConfirmationTarget) commit(ctx context.Context) error {
	return t.prepared.CommitInspected(ctx, t.observation)
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
	if source == nil || source.target == nil {
		return nil
	} // legacy memory seam; no product constructor
	t := source.target
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
	g.launch = work // ownership published before issuer callback or OS side effects
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
func (r *previewConfirmation) closeTarget(ctx context.Context) error {
	t := r.runtimeSource.target
	if t == nil {
		return nil
	}
	s := r.service
	s.mu.Lock()
	g := r.grant
	var done <-chan struct{}
	if g != nil && g.launch != nil {
		done = g.launch.done
	}
	s.mu.Unlock()
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
	return authCleanupCall(func() error { return t.ops.close(ctx) })
}
