package childjob

import (
	"context"
	"runtime"
	"strconv"
	"strings"
)

// ProtectedGuestConfig is an immutable descriptor candidate supplied by the
// protected factory. SID syntax and matching descriptors do not authenticate
// that factory, authorize a guest, or establish complete OS separation.
type ProtectedGuestConfig struct {
	service, guest string
	valid          bool
}

type protectedKind uint8

const (
	protectedToken protectedKind = iota
	protectedProcess
	protectedThread
	protectedJob
)

func protectedMask(kind protectedKind) uint32 {
	switch kind {
	case protectedToken:
		return 0x000f01ff
	case protectedProcess, protectedThread:
		return 0x001fffff
	case protectedJob:
		return 0x001f003f
	}
	return 0
}

type protectedACE struct {
	sid         string
	mask        uint32
	kind, flags uint8
}
type protectedSecurity struct {
	owner              string
	present, protected bool
	entries            []protectedACE
}
type protectedGuestKernel interface {
	newProtectedJob(ProtectedGuestConfig) (uintptr, error)
	objectSecurity(uintptr) (protectedSecurity, error)
}

func (c ProtectedGuestConfig) accepts(f protectedSecurity, kind protectedKind) bool {
	if !c.valid || f.owner != "S-1-5-18" || !f.present || !f.protected || len(f.entries) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, a := range f.entries {
		if a.kind != 0 || a.flags != 0 || a.mask != protectedMask(kind) || (a.sid != "S-1-5-18" && a.sid != c.service) || seen[a.sid] {
			return false
		}
		seen[a.sid] = true
	}
	return seen["S-1-5-18"] && seen[c.service]
}

func inspectSecurity(k protectedGuestKernel, c ProtectedGuestConfig, h uintptr, kind protectedKind) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrStart
		}
	}()
	if !ordinaryHandle(h) {
		return ErrStart
	}
	f, err := k.objectSecurity(h)
	if err != nil || !c.accepts(f, kind) {
		return ErrStart
	}
	return nil
}

// ProtectedGuestObservation is bound to one retained suspended target. It is
// not authentication, issuer approval or a complete guest-separation proof.
type ProtectedGuestObservation struct {
	owner      *Prepared
	inspection *Inspection
	token      *GuestTokenObservation
}

// MatchesGuestSID compares a current opaque observation with a separately
// supplied creation-receipt SID. It does not authenticate that receipt, replace
// same-issuer/target checks, or create execution authority.
func (g *ProtectedGuestObservation) MatchesGuestSID(sid string) bool {
	return g != nil && g.owner != nil && g.owner.spec.Protection != nil && g.owner.spec.Protection.guest == sid && g.owner.ProtectedGuestCurrent(g.inspection, g)
}

// Called only while the same target gate is held; it must not reenter the
// public GuestTokenCurrent method, which would acquire that gate again.
func (p *Prepared) protectedTokenBinding(i *Inspection) bool {
	g := p.guestObservation
	return p.spec.Protection != nil && g != nil && g.owner == p && g.inspection == i && i == p.inspection && g.expectation.valid && g.expectation.user == p.spec.Protection.guest
}

func (p *Prepared) protectedPair(ctx context.Context, query uintptr) error {
	k, ok := p.api.(protectedGuestKernel)
	if !ok || p.spec.Protection == nil {
		return ErrStart
	}
	for _, target := range []struct {
		h    uintptr
		kind protectedKind
	}{{p.spec.PrimaryToken, protectedToken}, {query, protectedToken}, {p.child.process, protectedProcess}, {p.child.thread, protectedThread}, {p.child.job, protectedJob}} {
		if !bounded(ctx) || !bounded(p.lease) || inspectSecurity(k, *p.spec.Protection, target.h, target.kind) != nil {
			return ErrStart
		}
	}
	return nil
}

// ObserveProtectedGuest reads kernel-object owners/DACLs on retained handles;
// TokenOwner information is not used as an object-owner substitute.
func (p *Prepared) ObserveProtectedGuest(ctx context.Context, i *Inspection) (*ProtectedGuestObservation, error) {
	if p == nil || p.child == nil || !bounded(ctx) || p.spec.Protection == nil {
		return nil, ErrConfig
	}
	if i == nil || i.owner != p || p.state.Load() != preparedInspected {
		return nil, ErrStart
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := p.child.take(ctx); err != nil {
		return nil, err
	}
	err := ErrStart
	if i == p.inspection && p.protectedObservation == nil && p.protectedTokenBinding(i) && p.state.Load() == preparedInspected {
		// Use the exact query handle that produced GuestTokenObservation, not
		// an earlier Inspect handle whose token could have since been replaced.
		err = p.protectedPair(ctx, p.guestQueryToken)
	}
	if err == nil && p.state.Load() == preparedInspected && bounded(ctx) && bounded(p.lease) {
		p.protectedObservation = &ProtectedGuestObservation{owner: p, inspection: i, token: p.guestObservation}
	} else {
		err = ErrStart
		p.state.Store(preparedRevoked)
	}
	observation := p.protectedObservation
	p.child.gate <- struct{}{}
	if err != nil {
		return nil, p.failedInspection()
	}
	return observation, nil
}

func (p *Prepared) ProtectedGuestCurrent(i *Inspection, g *ProtectedGuestObservation) bool {
	if p == nil || p.child == nil || i == nil || g == nil || g.owner != p || g.inspection != i || !bounded(p.lease) || p.state.Load() != preparedInspected {
		return false
	}
	select {
	case <-p.child.gate:
	default:
		return false
	}
	defer func() { p.child.gate <- struct{}{} }()
	return i == p.inspection && g == p.protectedObservation && g.token == p.guestObservation && p.protectedTokenBinding(i) && bounded(p.lease) && p.state.Load() == preparedInspected
}

func (p *Prepared) recheckProtectedGuest(ctx context.Context, query uintptr) error {
	if p.spec.Protection == nil {
		return nil
	}
	g := p.protectedObservation
	if g == nil || g.owner != p || g.inspection != p.inspection || g.token != p.guestObservation || !p.protectedTokenBinding(p.inspection) {
		return ErrStart
	}
	return p.protectedPair(ctx, query)
}

func NewProtectedGuestConfig(serviceSID, guestSID string) (ProtectedGuestConfig, error) {
	parts := strings.Split(serviceSID, "-")
	if len(parts) != 9 || strings.Join(parts[:4], "-") != "S-1-5-80" {
		return ProtectedGuestConfig{}, ErrConfig
	}
	for _, part := range parts[4:] {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return ProtectedGuestConfig{}, ErrConfig
		}
	}
	if _, ok := guestAccountSID(guestSID); !ok {
		return ProtectedGuestConfig{}, ErrConfig
	}
	return ProtectedGuestConfig{service: serviceSID, guest: guestSID, valid: true}, nil
}
