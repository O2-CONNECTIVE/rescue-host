package guestaccount

import (
	"context"
	"runtime"
)

// public NetAPIだけの小さいseam。Windows以外の試験では全callを合成する。
type permissionAliasesAPI interface {
	computer(*permissionAliasesMemory) error
	builtin(*permissionAliasesMemory) error
	enumerate(*permissionAliasPage) error
	lookup(*permissionAliasIdentity) error
	members(*permissionAliasPage) error
	size(*permissionAliasPage) error
	free(*permissionAliasPage) error
}
type permissionAliasPage struct {
	owner                        *permissionAliasesMemory
	base                         *byte
	entries, total, size, status uint32
	resume                       uintptr
	name                         string
	wide                         []uint16
}
type permissionAliasIdentity struct {
	owner                    *permissionAliasesMemory
	qualified                []uint16
	sid                      [68]byte
	domain                   [257]uint16
	sidSize, domainSize, use uint32
	found                    bool
}
type permissionAliasesMemory struct {
	api                                                      permissionAliasesAPI
	binding                                                  binding
	computerName, builtinName                                string
	computerBuffer, builtinBuffer, builtinDomain             [257]uint16
	computerSize, builtinSize, builtinDomainSize, builtinUse uint32
	builtinSID                                               [12]byte
	pages                                                    []*permissionAliasPage
	identities                                               []*permissionAliasIdentity
	groups                                                   []permissionGroup
	pins                                                     runtime.Pinner
	pinned, active, complete, rpcPending                     bool
}
type permissionAliasesKernel struct {
	api      permissionAliasesAPI
	inflight accountQueryMemory
}

func (k *permissionAliasesKernel) snapshot(ctx context.Context, b binding, guard func() bool) (accountQueryMemory, error) {
	m := &permissionAliasesMemory{api: k.api, binding: b}
	k.inflight = m
	return m, m.read(ctx, guard)
}
func (m *permissionAliasesMemory) decode() (accountQueryObservation, error) {
	if !m.complete || m.active || m.rpcPending {
		return accountQueryObservation{}, ErrDenied
	}
	groups, e := copyPermissionAliasGraph(m.groups)
	return accountQueryObservation{kind: accountQueryGroups, groups: groups}, e
}
func (m *permissionAliasesMemory) release(guard func() bool) error {
	if guard == nil || !guard() || m.active {
		return ErrUnknown
	}
	var result error
	for i := len(m.pages) - 1; i >= 0; i-- {
		p := m.pages[i]
		if p.base == nil {
			continue
		}
		if !guard() {
			return ErrUnknown
		}
		if e := m.api.free(p); e != nil {
			result = ErrCleanup
		} else {
			p.base = nil
		}
		if !guard() {
			return ErrUnknown
		}
	}
	if result != nil {
		return result
	}
	if m.rpcPending {
		return ErrCleanup
	}
	if m.pinned {
		m.pins.Unpin()
		m.pinned = false
	}
	return nil
}
func (m *permissionAliasesMemory) call(ctx context.Context, guard func() bool, fn func() error) error {
	if !finite(ctx) || guard == nil || !guard() {
		return ErrDenied
	}
	m.active = true
	e := fn()
	m.active = false
	if !finite(ctx) || !guard() {
		return ErrDenied
	}
	return e
}
func (m *permissionAliasesMemory) pin(pointer any) { m.pinned = true; m.pins.Pin(pointer) }
