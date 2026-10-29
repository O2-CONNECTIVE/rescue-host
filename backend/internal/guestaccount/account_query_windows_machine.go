//go:build windows

package guestaccount

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var accountQueryDsRole = accountQueryNetapi.NewProc("DsRoleGetPrimaryDomainInformation")
var accountQueryDsFree = accountQueryNetapi.NewProc("DsRoleFreeMemory")
var accountQueryAdvapi = windows.NewLazySystemDLL("advapi32.dll")
var accountQueryLsaOpen = accountQueryAdvapi.NewProc("LsaOpenPolicy")
var accountQueryLsaQuery = accountQueryAdvapi.NewProc("LsaQueryInformationPolicy")
var accountQueryLsaFree = accountQueryAdvapi.NewProc("LsaFreeMemory")
var accountQueryLsaClose = accountQueryAdvapi.NewProc("LsaClose")

// https://learn.microsoft.com/en-us/windows/win32/api/dsrole/nf-dsrole-dsrolegetprimarydomaininformation
func (k *accountQueryWindows) localRole() (accountQueryMemory, error) {
	m := &accountQueryRoleMemory{}
	k.inflight = m
	status, _, _ := accountQueryDsRole.Call(0, 1, uintptr(unsafe.Pointer(&m.base)))
	if m.base == nil {
		return nil, ErrDenied
	}
	if status != 0 {
		return m, ErrDenied
	}
	return m, nil
}

type accountQueryRoleMemory struct{ base unsafe.Pointer }

func (m *accountQueryRoleMemory) decode() (accountQueryObservation, error) {
	if m.base == nil {
		return accountQueryObservation{}, ErrDenied
	}
	// 成功したDSROLE_PRIMARY_DOMAIN_INFO_BASICの先頭MachineRoleだけを読む。
	return accountQueryObservation{kind: accountQueryRole, role: *(*uint32)(m.base)}, nil
}
func (m *accountQueryRoleMemory) release(scopeGuard func() bool) error {
	if m.base != nil {
		if scopeGuard == nil || !scopeGuard() {
			return ErrUnknown
		}
		accountQueryDsFree.Call(uintptr(m.base))
		m.base = nil
	}
	return nil // DsRoleFreeMemoryはVOID。成功の戻り値を捏造しない。
}

// SDK ABI: https://learn.microsoft.com/en-us/windows/win32/api/lsalookup/ns-lsalookup-lsa_object_attributes
type accountQueryLSAAttributes struct {
	length            uint32
	root, objectName  uintptr
	attributes        uint32
	security, quality uintptr
}
type accountQueryLSAString struct {
	length, maximum uint16
	buffer          *uint16
}
type accountQueryLSADomain struct {
	name accountQueryLSAString
	sid  *byte
}
type accountQueryLSAMemory struct {
	handle windows.Handle
	data   *accountQueryLSADomain
}

// PolicyAccountDomainInformationはlocal SAMのaccount domain。primary AD domainで代替しない。
// https://learn.microsoft.com/en-us/windows/win32/api/ntsecapi/nf-ntsecapi-lsaqueryinformationpolicy
// https://learn.microsoft.com/en-us/windows/win32/api/lsalookup/ns-lsalookup-policy_account_domain_info
func (k *accountQueryWindows) localDomain() (accountQueryMemory, error) {
	m := &accountQueryLSAMemory{}
	k.inflight = m
	attrs := accountQueryLSAAttributes{}
	attrs.length = uint32(unsafe.Sizeof(attrs))
	// local NULL / POLICY_VIEW_LOCAL_INFORMATION=1だけを要求する。
	status, _, _ := accountQueryLsaOpen.Call(0, uintptr(unsafe.Pointer(&attrs)), 1, uintptr(unsafe.Pointer(&m.handle)))
	if status != 0 || m.handle == 0 {
		if m.handle != 0 {
			return m, ErrDenied
		}
		return nil, ErrDenied
	}
	status, _, _ = accountQueryLsaQuery.Call(uintptr(m.handle), 5, uintptr(unsafe.Pointer(&m.data)))
	if status != 0 || m.data == nil {
		return m, ErrDenied
	}
	return m, nil
}
func (m *accountQueryLSAMemory) decode() (accountQueryObservation, error) {
	if m.data == nil || m.data.sid == nil {
		return accountQueryObservation{}, ErrDenied
	}
	// LSA outputはAPIのschema契約に従う。NetAPIのallocation-size保証とは別。
	header := unsafe.Slice(m.data.sid, 8)
	if header[0] != 1 || header[1] > 15 {
		return accountQueryObservation{}, ErrDenied
	}
	sid, err := accountQuerySID(unsafe.Slice(m.data.sid, 8+4*int(header[1])))
	if err != nil || !machineSID(sid) {
		return accountQueryObservation{}, ErrDenied
	}
	return accountQueryObservation{kind: accountQueryDomain, sid: sid}, nil
}
func (m *accountQueryLSAMemory) release(scopeGuard func() bool) error {
	result := error(nil)
	if m.data != nil {
		if scopeGuard == nil || !scopeGuard() {
			return ErrUnknown
		}
		status, _, _ := accountQueryLsaFree.Call(uintptr(unsafe.Pointer(m.data)))
		if status == 0 {
			m.data = nil
		} else {
			result = ErrCleanup
		}
	}
	if m.handle != 0 {
		if scopeGuard == nil || !scopeGuard() {
			return ErrUnknown
		}
		status, _, _ := accountQueryLsaClose.Call(uintptr(m.handle))
		if status == 0 {
			m.handle = 0
		} else {
			result = ErrCleanup
		}
	}
	return result
}
