package serviceprovision

import "context"

func runtimeGuestAnchor(base Anchor) (Anchor, error) {
	return guestAnchorPair(base, "guest-runtime-v1.json", "guest-runtime-v1.sha256")
}

// LoadRuntimeGuestConfiguration reads the separate Batch layout from the same
// protected root. It grants no approval and performs no account or logon action.
// A partial configuration remains owned until its actual Close completes.
func (p *Prepared) LoadRuntimeGuestConfiguration(ctx context.Context) (*GuestConfiguration, error) {
	if p == nil || p.loaded == nil || !p.accepted.Load() || !rootLive(ctx) {
		return nil, ErrRootDenied
	}
	a, err := runtimeGuestAnchor(p.loaded.anchor)
	if err != nil {
		return nil, err
	}
	r, err := NewNativeRoot(a)
	if err != nil {
		return nil, err
	}
	return p.loadRuntimeGuestConfiguration(ctx, r)
}

func (p *Prepared) loadRuntimeGuestConfiguration(ctx context.Context, r *Root) (*GuestConfiguration, error) {
	return p.loadGuestConfiguration(ctx, r, true)
}
