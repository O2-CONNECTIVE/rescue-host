//go:build windows

package guestaccount

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// factoryは純粋。公開入口、OS設定、資格情報、account mutationへ接続しない。
func newNativePermissionGroupsCollector(q *accountQuery) (*permissionGroupsCollector, error) {
	return newPermissionGroupsCollector(q, &permissionAliasesKernel{api: permissionAliasesWindows{}})
}

type permissionAliasesWindows struct{}

var permissionAliasesEnum = accountQueryNetapi.NewProc("NetLocalGroupEnum")
var permissionAliasesGetMembers = accountQueryNetapi.NewProc("NetLocalGroupGetMembers")

func (permissionAliasesWindows) computer(m *permissionAliasesMemory) error {
	m.pin(m)
	return windows.GetComputerName(&m.computerBuffer[0], &m.computerSize)
}
func (permissionAliasesWindows) builtin(m *permissionAliasesMemory) error {
	m.pin(m)
	return windows.LookupAccountSid(nil, (*windows.SID)(unsafe.Pointer(&m.builtinSID[0])), &m.builtinBuffer[0], &m.builtinSize, &m.builtinDomain[0], &m.builtinDomainSize, &m.builtinUse)
}

// MAX_PREFERRED_LENGTHの一回取得だけを支持する。MORE_DATAはmemoryに残り、
// bufferをfreeできても未終了RPC/resumeの回収ackは発行しない。
func (permissionAliasesWindows) enumerate(p *permissionAliasPage) error {
	p.owner.pin(p)
	status, _, _ := permissionAliasesEnum.Call(0, 0, uintptr(unsafe.Pointer(&p.base)), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&p.entries)), uintptr(unsafe.Pointer(&p.total)), uintptr(unsafe.Pointer(&p.resume)))
	p.status = uint32(status)
	return nil
}
func (permissionAliasesWindows) lookup(i *permissionAliasIdentity) error {
	i.owner.pin(i)
	i.owner.pin(&i.qualified[0])
	e := windows.LookupAccountName(nil, &i.qualified[0], (*windows.SID)(unsafe.Pointer(&i.sid[0])), &i.sidSize, &i.domain[0], &i.domainSize, &i.use)
	if errors.Is(e, windows.ERROR_NONE_MAPPED) {
		return nil
	}
	if e != nil {
		return e
	}
	i.found = true
	return nil
}

// level0はSIDだけを返し、表示名解決で未解決memberを落とさない。
// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netlocalgroupgetmembers
func (permissionAliasesWindows) members(p *permissionAliasPage) error {
	p.owner.pin(p)
	p.owner.pin(&p.wide[0])
	status, _, _ := permissionAliasesGetMembers.Call(0, uintptr(unsafe.Pointer(&p.wide[0])), 0, uintptr(unsafe.Pointer(&p.base)), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&p.entries)), uintptr(unsafe.Pointer(&p.total)), uintptr(unsafe.Pointer(&p.resume)))
	p.status = uint32(status)
	return nil
}
func (permissionAliasesWindows) size(p *permissionAliasPage) error {
	p.owner.pin(p)
	status, _, _ := accountQueryBufferSize.Call(uintptr(unsafe.Pointer(p.base)), uintptr(unsafe.Pointer(&p.size)))
	if status != 0 {
		return ErrDenied
	}
	return nil
}
func (permissionAliasesWindows) free(p *permissionAliasPage) error {
	return windows.NetApiBufferFree(p.base)
}
