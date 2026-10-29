//go:build windows

package guestaccount

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type permissionFactsWindows struct{ inflight accountQueryMemory }

func newNativePermissionFactsCollector(q *accountQuery) (*permissionFactsCollector, error) {
	return newPermissionFactsCollector(q, &permissionFactsWindows{})
}

type permissionFactsUser4Memory struct {
	accountQueryNetMemory // 既存の guarded NetApiBufferFree を共用する。
	binding               binding
	phase                 permissionFactsPhase
	name                  *uint16
	pins                  runtime.Pinner
}

func (k *permissionFactsWindows) user4(b binding, phase permissionFactsPhase) (accountQueryMemory, error) {
	m := &permissionFactsUser4Memory{binding: b, phase: phase}
	k.inflight = m
	var e error
	m.name, e = windows.UTF16PtrFromString(b.name)
	if e != nil {
		return nil, ErrDenied
	}
	if !phase.allowed() {
		return m, ErrDenied
	}
	m.pins.Pin(m.name)
	m.pins.Pin(&m.base)
	e = windows.NetUserGetInfo(nil, m.name, 4, &m.base)
	m.pins.Unpin()
	if !phase.allowed() || m.base == nil {
		return m, ErrDenied
	}
	return m, e
}
func (m *permissionFactsUser4Memory) decode() (accountQueryObservation, error) {
	if !m.phase.allowed() || m.base == nil {
		return accountQueryObservation{}, ErrDenied
	}
	var size uint32
	status, _, _ := accountQueryBufferSize.Call(uintptr(unsafe.Pointer(m.base)), uintptr(unsafe.Pointer(&size)))
	if !m.phase.allowed() || status != 0 {
		return accountQueryObservation{}, ErrDenied
	}
	return decodePermissionUser4(m.base, size, m.binding)
}

// raw guard は request の cancel に依存させない。元 query の回収規約を保つ。
func (m *permissionFactsUser4Memory) release(guard func() bool) error {
	e := m.accountQueryNetMemory.release(guard)
	if guard == nil || !guard() {
		return ErrUnknown
	}
	return e
}

var permissionFactsEnumerateRights = accountQueryAdvapi.NewProc("LsaEnumerateAccountRights")

type permissionFactsRightsMemory struct {
	handle        windows.Handle
	data          *accountQueryLSAString
	count, status uint32
	enumerated    bool
	sid           []byte
	sidText       string
	attrs         accountQueryLSAAttributes
	pins          runtime.Pinner
}

// LSA_UNICODE_STRING は SDK の USHORT/USHORT/pointer。Win32=8、Win64=16。
var _ [unsafe.Sizeof(accountQueryLSAString{}) - 2*unsafe.Sizeof(uintptr(0))]byte
var _ [2*unsafe.Sizeof(uintptr(0)) - unsafe.Sizeof(accountQueryLSAString{})]byte

func (k *permissionFactsWindows) rights(sid string, phase permissionFactsPhase) (accountQueryMemory, error) {
	m := &permissionFactsRightsMemory{sidText: sid, status: ^uint32(0)}
	k.inflight = m
	var e error
	m.sid, e = permissionFactsSID(sid)
	if e != nil {
		return nil, e
	}
	m.attrs.length = uint32(unsafe.Sizeof(m.attrs))
	if !phase.allowed() {
		return m, ErrDenied
	}
	m.pins.Pin(&m.attrs)
	m.pins.Pin(&m.handle)
	// local NULL / POLICY_LOOKUP_NAMES=0x800 だけ。account/token を作成しない。
	status, _, _ := accountQueryLsaOpen.Call(0, uintptr(unsafe.Pointer(&m.attrs)), 0x800, uintptr(unsafe.Pointer(&m.handle)))
	m.pins.Unpin()
	if !phase.allowed() || uint32(status) != 0 || m.handle == 0 {
		return m, ErrDenied
	}
	if !phase.allowed() {
		return m, ErrDenied
	}
	m.pins.Pin(&m.sid[0])
	m.pins.Pin(&m.data)
	m.pins.Pin(&m.count)
	status, _, _ = permissionFactsEnumerateRights.Call(uintptr(m.handle), uintptr(unsafe.Pointer(&m.sid[0])), uintptr(unsafe.Pointer(&m.data)), uintptr(unsafe.Pointer(&m.count)))
	m.status = uint32(status)
	m.enumerated = true
	m.pins.Unpin()
	if !phase.allowed() {
		return m, ErrDenied
	}
	if _, e = permissionFactsRightsStatus(m.status, m.count, m.data != nil); e != nil {
		return m, e
	}
	return m, nil
}
func (m *permissionFactsRightsMemory) decode() (accountQueryObservation, error) {
	if !m.enumerated {
		return accountQueryObservation{}, ErrDenied
	}
	empty, e := permissionFactsRightsStatus(m.status, m.count, m.data != nil)
	if e != nil {
		return accountQueryObservation{}, e
	}
	out := accountQueryObservation{kind: accountQueryRights, sid: m.sidText, rights: []string{}}
	if empty {
		return out, nil
	}
	// LSA 返却の schema/count/length を使い、NetAPI の allocation-size 境界と区別する。
	base := uintptr(unsafe.Pointer(m.data))
	size := uintptr(m.count) * unsafe.Sizeof(accountQueryLSAString{})
	if base == 0 || base+size < base {
		return accountQueryObservation{}, ErrDenied
	}
	for _, s := range unsafe.Slice(m.data, int(m.count)) {
		v, e := permissionFactsRightBuffer(s.length, s.maximum, s.buffer)
		if e != nil {
			return accountQueryObservation{}, e
		}
		out.rights = append(out.rights, v)
	}
	out.rights, e = copyPermissionRights(out.rights)
	if e != nil {
		return accountQueryObservation{}, e
	}
	return out, nil
}
func (m *permissionFactsRightsMemory) release(guard func() bool) error {
	var result error
	if m.data != nil {
		if guard == nil || !guard() {
			return ErrUnknown
		}
		status, _, _ := accountQueryLsaFree.Call(uintptr(unsafe.Pointer(m.data)))
		if status == 0 {
			m.data = nil
		} else {
			result = ErrCleanup
		}
		if !guard() {
			return ErrUnknown
		}
	}
	if m.handle != 0 {
		if guard == nil || !guard() {
			return ErrUnknown
		}
		status, _, _ := accountQueryLsaClose.Call(uintptr(m.handle))
		if status == 0 {
			m.handle = 0
		} else {
			result = ErrCleanup
		}
		if !guard() {
			return ErrUnknown
		}
	}
	return result
}
