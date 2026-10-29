package guestaccount

import "context"

// recoverLifecycleは保護journalと旧対象producer専用。再作成・再有効化しない。
func recoverLifecycle(ctx context.Context, p Plan, s sources) (owner *Lifecycle, result error) {
	defer func() {
		if recover() != nil {
			owner = nil
			result = ErrUnknown
		}
	}()
	if !finite(ctx) {
		return nil, ErrDenied
	}
	if !configured(p, s) {
		return nil, ErrUnconfigured
	}
	l := &Lifecycle{plan: p, sources: s}
	l.self = l
	r, err := s.journal.load(ctx)
	if err != nil || r.seal != s.journalSeal || !finite(ctx) {
		return nil, ErrUnknown
	}
	b := r.record.binding
	if r.record.stage < reserved || r.record.stage > deleteComplete {
		return nil, ErrManual
	}
	if r.record.stage < ownedSID {
		if b != p.binding {
			return nil, ErrManual
		}
	} else if !p.validBinding(b) {
		return nil, ErrManual
	}
	if r.record.stage != deleteComplete && (s.previous == nil || s.previous.previousBinding() != b) {
		return nil, ErrUnconfigured
	}
	l.record = r.record
	l.recovering = true
	l.stop.Store(true)
	l.state = Cleaning
	if r.record.stage == createAttempt {
		l.state = Quarantined
	}
	if r.record.stage == deleteAttempt {
		l.deletionPending = true
		l.unknown = true
		l.state = Unknown
	}
	if r.record.stage == deleteComplete {
		l.deletionObserved = true
		l.state = Closed
	}
	return l, nil
}

func (l *Lifecycle) revoke(ctx context.Context) (sessionReceipt, error) {
	if l.recovering {
		if l.sources.previous == nil || l.sources.previous.previousBinding() != l.record.binding {
			return sessionReceipt{}, ErrUnconfigured
		}
		return l.sources.previous.revokePrevious(ctx, l.record.binding)
	}
	return l.sources.session.revoke(ctx, l.record.binding)
}
func (l *Lifecycle) quiesce(ctx context.Context) (sessionReceipt, error) {
	if l.recovering {
		if l.sources.previous == nil || l.sources.previous.previousBinding() != l.record.binding {
			return sessionReceipt{}, ErrUnconfigured
		}
		return l.sources.previous.quiescePrevious(ctx, l.record.binding)
	}
	return l.sources.session.quiesce(ctx, l.record.binding)
}
