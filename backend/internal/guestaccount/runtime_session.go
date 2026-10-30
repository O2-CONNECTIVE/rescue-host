package guestaccount

import "context"

func (r *RuntimeGuest) bindCreated(receipt accountReceipt, expected *seal) error {
	if r == nil || r.self != r || r.lifecycle == nil || expected == nil || expected != r.lifecycle.sources.accountSeal || receipt.seal != expected || receipt.kind != created || !r.plan.validBinding(receipt.binding) {
		return ErrUnknown
	}
	if r.created.sid != "" && r.created != receipt.binding {
		return ErrUnknown
	}
	r.created = receipt.binding // preserve the actual SID even if ticket binding fails
	return r.authority.bind(contextForRuntimeBinding(r), r.created)
}

// Lifecycle's creator callback has no context. Prepare keeps its bounded context
// in the original owner; native authority binding never creates a fresh budget.
func contextForRuntimeBinding(r *RuntimeGuest) context.Context { return r.prepareContext }

func (r *RuntimeGuest) prepareOwnedAccount(ctx context.Context, b binding) error {
	if r == nil || r.created != b || r.rights == nil || r.lifecycle.record.stage != ownedSID || !r.live(ctx) {
		return ErrDenied
	}
	if err := r.rights.provision(ctx, b, r.plan.policy.logonRights, r.ops.guard); err != nil || !r.live(ctx) {
		return ErrDenied
	}
	return nil
}
func (r *RuntimeGuest) revoke(ctx context.Context, b binding) (sessionReceipt, error) {
	if r == nil || r.created.sid != "" && b != r.created || r.created.sid == "" && b != r.plan.binding {
		return sessionReceipt{}, ErrDenied
	}
	_ = r.Revoke()
	if err := r.authority.revoke(ctx, b); err != nil {
		return sessionReceipt{}, err
	}
	return sessionReceipt{r.sessionSeal, b, revoked}, nil
}
func (r *RuntimeGuest) quiesce(ctx context.Context, b binding) (sessionReceipt, error) {
	if r == nil || b != r.created || !finite(ctx) {
		return sessionReceipt{}, ErrCleanup
	}
	if err := r.closeRuntime(ctx); err != nil {
		return sessionReceipt{}, err
	}
	return sessionReceipt{r.sessionSeal, b, quiescent}, nil
}
func (r *RuntimeGuest) closeRuntime(ctx context.Context) error {
	for _, held := range []creationOwned{r.target, r.credential, r.token, r.rights} {
		if held != nil {
			if !finite(ctx) || !r.ops.guard() {
				return ErrCleanup
			}
			if err := held.Close(ctx); err != nil {
				return err
			}
		}
	}
	clearRuntimeSecret(r.secret)
	return nil
}
