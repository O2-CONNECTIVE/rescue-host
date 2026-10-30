package childjob

import (
	"context"
	"runtime"
)

// Inspection is an opaque observation of one held target. It is not approval,
// a nonadmin proof, a grant, or an identity that may be serialized/restored.
type Inspection struct{ owner *Prepared }

// Inspect retains the same suspended process/thread/Job and its query token.
// It never resumes the target. The original owner remains responsible for Close.
func (p *Prepared) Inspect(ctx context.Context) (*Inspection, error) {
	if p == nil || p.child == nil || !bounded(ctx) {
		return nil, ErrConfig
	}
	if p.state.Load() != preparedStaged {
		return nil, ErrStart
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := p.child.take(ctx); err != nil {
		return nil, err
	}
	var err error
	if p.state.Load() != preparedStaged || !bounded(p.lease) {
		err = ErrStart
	} else {
		p.queryToken, err = p.api.inspectTarget(p.spec, p.child.process, p.child.thread, p.child.job)
		if err != nil || !ordinaryHandle(p.queryToken) || !bounded(ctx) || !bounded(p.lease) || !p.state.CompareAndSwap(preparedStaged, preparedInspected) {
			err = ErrStart
		}
	}
	if err != nil {
		p.state.Store(preparedRevoked)
	} else {
		p.inspection = &Inspection{owner: p}
	}
	receipt := p.inspection
	p.child.gate <- struct{}{}
	if err != nil {
		return nil, p.failedInspection()
	}
	return receipt, nil
}

// CommitInspected consumes only the inspection retained by this exact Prepared.
// The protected owner must obtain real approval separately before calling it.
// This method does not authenticate a caller or create authority.
func (p *Prepared) CommitInspected(ctx context.Context, receipt *Inspection) error {
	if p == nil || p.child == nil || !bounded(ctx) {
		return ErrConfig
	}
	if receipt == nil || receipt.owner != p || p.state.Load() != preparedInspected {
		return ErrStart
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := p.child.take(ctx); err != nil {
		return err
	}
	err := ErrStart
	if receipt == p.inspection && p.state.Load() == preparedInspected && bounded(ctx) && bounded(p.lease) {
		// Reobserve the same retained target immediately before the admission CAS.
		// Both query references remain owned even when the second inspection fails.
		p.commitQueryToken, err = p.api.inspectTarget(p.spec, p.child.process, p.child.thread, p.child.job)
		if err == nil && ordinaryHandle(p.commitQueryToken) && p.recheckGuestToken(ctx, p.commitQueryToken) == nil && p.recheckProtectedGuest(ctx, p.commitQueryToken) == nil && bounded(ctx) && bounded(p.lease) && p.state.CompareAndSwap(preparedInspected, preparedStarted) {
			err = p.child.resumeOwned(ctx)
			if err == nil && (p.state.Load() != preparedStarted || !bounded(ctx) || !bounded(p.lease)) {
				err = ErrStart
			}
		} else {
			err = ErrStart
		}
	}
	if err != nil {
		p.state.Store(preparedRevoked)
	}
	p.child.gate <- struct{}{}
	if err != nil {
		return p.failedInspection()
	}
	return nil
}

func (p *Prepared) failedInspection() error {
	cleanup, cancel := context.WithTimeout(context.Background(), p.spec.CleanupTimeout)
	defer cancel()
	if p.Close(cleanup) != nil {
		return ErrUnconfirmed
	}
	return ErrStart
}
