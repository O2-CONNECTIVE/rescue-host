package guestaccount

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

// Expectation is limited verification material from this owner's actual creator
// receipt. It is not authentication, protected DACL evidence or a runtime proof.
func (r *RuntimeGuest) Expectation(ctx context.Context) (childjob.GuestTokenExpectation, error) {
	if r == nil || r.self != r || !finite(ctx) || !r.mu.TryLock() {
		return childjob.GuestTokenExpectation{}, ErrDenied
	}
	defer r.mu.Unlock()
	if r.closed || r.fatal || r.stop.Load() || r.prepared.Load() == nil || r.lifecycle == nil || r.created != r.lifecycle.record.binding || !r.plan.validBinding(r.created) || !r.live(ctx) {
		return childjob.GuestTokenExpectation{}, ErrDenied
	}
	return childjob.NewBatchGuestTokenExpectation(r.created.sid, r.plan.policy.groups, r.plan.policy.privileges)
}
