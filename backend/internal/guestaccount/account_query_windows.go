//go:build windows

package guestaccount

import (
	"encoding/binary"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// このprivate factoryは読取診断だけを構成し、public NewNativeを開かない。
func newNativeAccountQuery(p Plan, b binding, s *seal) (*accountQuery, error) {
	return newAccountQuery(p, b, s, &accountQueryWindows{})
}

// source内panicでも、dispatch前に置いたout-parameter所有参照を失わない。
type accountQueryWindows struct{ inflight accountQueryMemory }

func (accountQueryWindows) threadID() uint32 { return windows.GetCurrentThreadId() }
func accountQueryCleanThread() bool {
	var value, size uint32
	return windows.GetTokenInformation(windows.GetCurrentThreadToken(), windows.TokenType, (*byte)(unsafe.Pointer(&value)), 4, &size) == windows.ERROR_NO_TOKEN
}
func accountQueryTokenDWORD(token windows.Token, class uint32) (uint32, error) {
	var value, size uint32
	if windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), 4, &size) != nil || size != 4 {
		return 0, ErrDenied
	}
	return value, nil
}
func (accountQueryWindows) principal() (accountQueryPrincipal, error) {
	if !accountQueryCleanThread() {
		return accountQueryPrincipal{threadToken: true}, ErrDenied
	}
	// query-only pseudo handleであり、real token作成/closeやimpersonationをしない。
	token := windows.GetCurrentProcessToken()
	kind, err := accountQueryTokenDWORD(token, windows.TokenType)
	if err != nil || kind != 1 {
		return accountQueryPrincipal{}, ErrDenied
	}
	restricted, err := accountQueryTokenDWORD(token, windows.TokenHasRestrictions)
	if err != nil {
		return accountQueryPrincipal{}, err
	}
	// SDK TOKEN_INFORMATION_CLASS.TokenIsAppContainer=29。未対応も拒否。
	// https://learn.microsoft.com/en-us/windows/win32/api/winnt/ne-winnt-token_information_class
	app, err := accountQueryTokenDWORD(token, 29)
	if err != nil {
		return accountQueryPrincipal{}, err
	}
	var stats [56]byte
	var size uint32
	if windows.GetTokenInformation(token, windows.TokenStatistics, &stats[0], 56, &size) != nil || size != 56 || binary.LittleEndian.Uint32(stats[24:28]) != kind {
		return accountQueryPrincipal{}, ErrDenied
	}
	var storage [256]byte
	var pins runtime.Pinner
	pins.Pin(&storage[0])
	defer pins.Unpin()
	if windows.GetTokenInformation(token, windows.TokenUser, &storage[0], uint32(len(storage)), &size) != nil || size < uint32(unsafe.Sizeof(windows.Tokenuser{})) || size > uint32(len(storage)) {
		return accountQueryPrincipal{}, ErrDenied
	}
	data := storage[:size]
	offset, ok := accountQueryOffset(accountQueryPointer(data, 0), uintptr(unsafe.Pointer(&storage[0])), len(data), 8)
	if !ok {
		return accountQueryPrincipal{}, ErrDenied
	}
	sid, err := accountQuerySID(data[offset:])
	if err != nil || !accountQueryCleanThread() {
		return accountQueryPrincipal{}, ErrDenied
	}
	return accountQueryPrincipal{user: sid, kind: kind, restricted: restricted != 0, appContainer: app != 0, tokenID: binary.LittleEndian.Uint64(stats[:8]), authenticationID: binary.LittleEndian.Uint64(stats[8:16]), modifiedID: binary.LittleEndian.Uint64(stats[48:56])}, nil
}

var accountQueryNetapi = windows.NewLazySystemDLL("netapi32.dll")
var accountQueryBufferSize = accountQueryNetapi.NewProc("NetApiBufferSize")

// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netusergetinfo
func (k *accountQueryWindows) user23(name string) (accountQueryMemory, error) {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, ErrDenied
	}
	m := &accountQueryNetMemory{}
	k.inflight = m
	err = windows.NetUserGetInfo(nil, wide, 23, &m.base)
	runtime.KeepAlive(wide)
	if m.base == nil {
		return nil, ErrDenied
	}
	return m, err
}

type accountQueryNetMemory struct{ base *byte }

func (m *accountQueryNetMemory) decode() (accountQueryObservation, error) {
	if m.base == nil {
		return accountQueryObservation{}, ErrDenied
	}
	var size uint32
	status, _, _ := accountQueryBufferSize.Call(uintptr(unsafe.Pointer(m.base)), uintptr(unsafe.Pointer(&size)))
	if status != 0 {
		return accountQueryObservation{}, ErrDenied
	}
	return decodeAccountUser23(m.base, size)
}
func (m *accountQueryNetMemory) release(scopeGuard func() bool) error {
	if m.base == nil {
		return nil
	}
	if scopeGuard == nil || !scopeGuard() {
		return ErrUnknown
	}
	if windows.NetApiBufferFree(m.base) != nil {
		return ErrCleanup
	}
	m.base = nil
	return nil
}
