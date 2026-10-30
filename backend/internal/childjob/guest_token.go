package childjob

import (
	"context"
	"runtime"
	"slices"
)

// GuestTokenObservation establishes limited facts, not authentication, a primary
// token's legitimate origin, protected DACLs, or SeparatedNonAdminGuest authority.
// Its target/lifetime stay with Prepared; issuer binding stays with the existing
// Service owner. Copying, serializing or moving it creates no usable evidence.
type GuestTokenObservation struct {
	owner          *Prepared
	inspection     *Inspection
	expectation    GuestTokenExpectation
	source, target guestTokenFacts
}
type guestTokenGroup struct {
	sid        string
	attributes uint32
}
type guestTokenPrivilege struct {
	name       string
	attributes uint32
	luid       uint64
}
type guestTokenFacts struct {
	user, owner, primaryGroup                    string
	authentication, tokenID, modifiedID          uint64
	kind, session, integrity, elevationType      uint32
	elevated, uiAccess, restricted, appContainer bool
	groups                                       []guestTokenGroup
	privileges                                   []guestTokenPrivilege
}
type guestTokenKernel interface {
	tokenFacts(uintptr) (guestTokenFacts, error)
}

func cloneGuestTokenFacts(f guestTokenFacts) guestTokenFacts {
	f.groups = append([]guestTokenGroup{}, f.groups...)
	f.privileges = append([]guestTokenPrivilege{}, f.privileges...)
	return f
}
func sameGuestTokenFacts(a, b guestTokenFacts) bool {
	ag, bg, ap, bp := a.groups, b.groups, a.privileges, b.privileges
	return a.user == b.user && a.owner == b.owner && a.primaryGroup == b.primaryGroup && a.authentication == b.authentication && a.tokenID == b.tokenID && a.modifiedID == b.modifiedID && a.kind == b.kind && a.session == b.session && a.integrity == b.integrity && a.elevationType == b.elevationType && a.elevated == b.elevated && a.uiAccess == b.uiAccess && a.restricted == b.restricted && a.appContainer == b.appContainer && slices.Equal(ag, bg) && slices.Equal(ap, bp)
}
func readGuestTokenFacts(k guestTokenKernel, h uintptr) (f guestTokenFacts, err error) {
	defer func() {
		if recover() != nil {
			err = ErrStart
		}
	}()
	return k.tokenFacts(h)
}
func (p *Prepared) guestPair(ctx context.Context, k guestTokenKernel, e GuestTokenExpectation, query uintptr) (guestTokenFacts, guestTokenFacts, error) {
	source, err := readGuestTokenFacts(k, p.spec.PrimaryToken)
	if err != nil || !bounded(ctx) || !bounded(p.lease) || !e.accepts(source) {
		return guestTokenFacts{}, guestTokenFacts{}, ErrStart
	}
	target, err := readGuestTokenFacts(k, query)
	if err != nil || !bounded(ctx) || !bounded(p.lease) || !e.accepts(target) || source.user != target.user || source.authentication != target.authentication || source.session != target.session {
		return source, target, ErrStart
	}
	return source, target, nil
}

// ObserveGuestToken queries only tokens opened from the held target and the
// retained borrowed primary token. It creates no logon/token or execution grant.
func (p *Prepared) ObserveGuestToken(ctx context.Context, i *Inspection, e GuestTokenExpectation) (*GuestTokenObservation, error) {
	if p == nil || p.child == nil || !e.valid || !bounded(ctx) {
		return nil, ErrConfig
	}
	k, ok := p.api.(guestTokenKernel)
	if !ok || i == nil || i.owner != p || p.state.Load() != preparedInspected {
		return nil, ErrStart
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := p.child.take(ctx); err != nil {
		return nil, err
	}
	err := ErrStart
	var source, target guestTokenFacts
	if i == p.inspection && p.guestObservation == nil && bounded(p.lease) && p.state.Load() == preparedInspected {
		p.guestQueryToken, err = p.api.inspectTarget(p.spec, p.child.process, p.child.thread, p.child.job)
		if err == nil && ordinaryHandle(p.guestQueryToken) {
			source, target, err = p.guestPair(ctx, k, e, p.guestQueryToken)
		} else {
			err = ErrStart
		}
	}
	if err == nil && p.state.Load() == preparedInspected && bounded(ctx) && bounded(p.lease) {
		e.groups = append([]string{}, e.groups...)
		e.privileges = append([]string{}, e.privileges...)
		p.guestObservation = &GuestTokenObservation{owner: p, inspection: i, expectation: e, source: cloneGuestTokenFacts(source), target: cloneGuestTokenFacts(target)}
	} else {
		err = ErrStart
		p.state.Store(preparedRevoked)
	}
	observation := p.guestObservation
	p.child.gate <- struct{}{}
	if err != nil {
		return nil, p.failedInspection()
	}
	return observation, nil
}
func (p *Prepared) recheckGuestToken(ctx context.Context, query uintptr) error {
	r := p.guestObservation
	if r == nil {
		return nil
	} // existing generic path makes no guest-policy claim
	k, ok := p.api.(guestTokenKernel)
	if !ok || r.owner != p || r.inspection != p.inspection {
		return ErrStart
	}
	source, target, err := p.guestPair(ctx, k, r.expectation, query)
	if err != nil || !sameGuestTokenFacts(source, r.source) || !sameGuestTokenFacts(target, r.target) {
		return ErrStart
	}
	return nil
}

// GuestTokenCurrent is local identity/lifetime observation only. Its true value
// does not establish issuer approval or physical guest separation.
func (p *Prepared) GuestTokenCurrent(i *Inspection, g *GuestTokenObservation) bool {
	if p == nil || p.child == nil || g == nil || i == nil || g.owner != p || g.inspection != i || !bounded(p.lease) || p.state.Load() != preparedInspected {
		return false
	}
	select {
	case <-p.child.gate:
	default:
		return false
	}
	defer func() { p.child.gate <- struct{}{} }()
	return i == p.inspection && g == p.guestObservation && bounded(p.lease) && p.state.Load() == preparedInspected
}
