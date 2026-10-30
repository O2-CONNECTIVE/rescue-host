package servicehost

// The dispatcher captures this exact retained grant only after confirmation and
// its reservation have finished. Preparation-only owners produce no continuation.
func (s *Service) runtimeGuestEvaluationGrant(d *serviceDispatcher, approvalID string) (*confirmedGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.preview == nil || s.preview.runtimeGuest == nil || !s.preview.runtimeGuest.evaluation {
		return nil, nil
	}
	a, g := s.preview.runtimeGuest, s.preview.grant
	if !a.matchesRegistrationLocked() || !s.confirmedTargetMatchesLocked(g) || g.approvalID != approvalID || g.confirmation.dispatcher != d || s.auth == nil || g.confirmation.request != s.auth.active {
		return nil, ErrProvisioningRejected
	}
	return g, nil
}

// exchangeBody calls this only after the real response completion succeeded.
// The same auth worker owns the synchronous commit; no detached worker, new
// authority, reconstructed binding or public target-selection API is introduced.
// The written response remains an approval receipt, never execution success.
func (r *serviceAuthRequest) continueRuntimeGuestEvaluation(reply *serviceDispatchReply) error {
	if reply == nil {
		return ErrProvisioningRejected
	}
	if reply.evaluation == nil {
		return nil
	}
	s := r.adapter.service
	s.mu.Lock()
	g := reply.evaluation
	valid := r.reply == reply && reply.owner == r.adapter.dispatch && r.liveLocked() && r.batchConsumed && s.preview != nil && s.preview.runtimeGuest != nil && s.preview.runtimeGuest.evaluation && s.preview.runtimeGuest.matchesRegistrationLocked() && s.confirmedTargetMatchesLocked(g) && g.confirmation.request == r
	s.mu.Unlock()
	if !valid {
		s.stopFor(ErrRuntimeUnconfirmed)
		return ErrRuntimeUnconfirmed
	}
	return s.launchConfirmedTarget(r.worker, g)
}
