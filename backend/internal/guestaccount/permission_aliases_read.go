package guestaccount

import (
	"context"
	"unicode/utf16"
)

func (m *permissionAliasesMemory) read(ctx context.Context, guard func() bool) error {
	if m.api == nil || !machineSID(m.binding.machine) {
		return ErrDenied
	}
	m.computerSize = uint32(len(m.computerBuffer))
	if e := m.call(ctx, guard, func() error { return m.api.computer(m) }); e != nil {
		return e
	}
	var e error
	m.computerName, e = permissionAliasNamespace(m.computerBuffer[:], m.computerSize)
	if e != nil {
		return e
	}
	m.builtinSID = [12]byte{1, 1, 0, 0, 0, 0, 0, 5, 32, 0, 0, 0}
	m.builtinSize = uint32(len(m.builtinBuffer))
	m.builtinDomainSize = uint32(len(m.builtinDomain))
	if e = m.call(ctx, guard, func() error { return m.api.builtin(m) }); e != nil {
		return e
	}
	if m.builtinUse != 3 {
		return ErrDenied
	}
	m.builtinName, e = permissionAliasLookupNamespace(m.builtinBuffer[:], m.builtinSize)
	if e != nil {
		return e
	}
	p, e := m.page(ctx, "", guard)
	if e != nil {
		return e
	}
	names, e := permissionAliasNames(p)
	if e != nil {
		return e
	}
	m.groups = make([]permissionGroup, 0, len(names))
	seen := map[string]bool{}
	edges := 0
	for _, name := range names {
		sid, e := m.resolve(ctx, name, guard)
		if e != nil || sid == "" || seen[sid] {
			return ErrDenied
		}
		seen[sid] = true
		p, e = m.page(ctx, name, guard)
		if e != nil {
			return e
		}
		members, e := permissionAliasMembers(p)
		if e != nil {
			return e
		}
		edges += len(members)
		if edges > 256 {
			return ErrDenied
		}
		after, e := m.resolve(ctx, name, guard)
		if e != nil || after != sid {
			return ErrDenied
		}
		m.groups = append(m.groups, permissionGroup{sid: sid, members: members})
	}
	if !finite(ctx) || !guard() {
		return ErrDenied
	}
	m.complete = true
	return nil
}
func (m *permissionAliasesMemory) page(ctx context.Context, name string, guard func() bool) (*permissionAliasPage, error) {
	p := &permissionAliasPage{owner: m, name: name, status: ^uint32(0)}
	m.pages = append(m.pages, p)
	if name != "" {
		p.wide = append(utf16.Encode([]rune(name)), 0)
	}
	e := m.call(ctx, guard, func() error {
		if name == "" {
			return m.api.enumerate(p)
		}
		return m.api.members(p)
	})
	if p.status == 234 {
		m.rpcPending = true
	}
	if e != nil {
		return p, e
	}
	if p.status != 0 {
		return p, ErrDenied
	}
	if p.base != nil {
		if e = m.call(ctx, guard, func() error { return m.api.size(p) }); e != nil {
			return p, e
		}
	}
	return p, nil
}
func (m *permissionAliasesMemory) resolve(ctx context.Context, name string, guard func() bool) (string, error) {
	sid := ""
	for _, ns := range []string{m.computerName, m.builtinName} {
		i := &permissionAliasIdentity{owner: m, qualified: append(utf16.Encode([]rune(ns+"\\"+name)), 0), sidSize: 68, domainSize: 257}
		m.identities = append(m.identities, i)
		if e := m.call(ctx, guard, func() error { return m.api.lookup(i) }); e != nil {
			return "", e
		}
		found, e := permissionAliasIdentitySID(i, m)
		if e != nil {
			return "", e
		}
		if found != "" {
			if sid != "" && sid != found {
				return "", ErrDenied
			}
			sid = found
		}
	}
	if sid == "" {
		return "", ErrDenied
	}
	return sid, nil
}
