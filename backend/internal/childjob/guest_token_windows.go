//go:build windows

package childjob

import (
	"encoding/binary"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TOKEN_INFORMATION_CLASS value missing from the pinned x/sys enumeration.
const guestTokenIsAppContainer uint32 = 29

// Every buffer has a fixed reviewed bound. Returned pointers are interpreted
// only while the original buffer is pinned; no token/group/privilege is changed.
func withGuestTokenInfo(token windows.Token, class uint32, size int, read func([]byte, uint64) error) error {
	if size < 4 || size > 65536 || unsafe.Sizeof(uintptr(0)) != 8 {
		return ErrStart
	}
	storage := make([]uint64, (size+7)/8)
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), size)
	var pin runtime.Pinner
	pin.Pin(&storage[0])
	defer pin.Unpin()
	var returned uint32
	if windows.GetTokenInformation(token, class, &data[0], uint32(size), &returned) != nil || returned > uint32(size) || returned == 0 {
		return ErrStart
	}
	return read(data[:returned], uint64(uintptr(unsafe.Pointer(&data[0]))))
}
func guestTokenDWORD(token windows.Token, class uint32) (value uint32, err error) {
	err = withGuestTokenInfo(token, class, 4, func(data []byte, _ uint64) error {
		if len(data) != 4 {
			return ErrStart
		}
		value = binary.LittleEndian.Uint32(data)
		return nil
	})
	return
}
func guestTokenSID(token windows.Token, class uint32, header int) (value string, err error) {
	err = withGuestTokenInfo(token, class, 256, func(data []byte, base uint64) error {
		if len(data) < header {
			return ErrStart
		}
		var e error
		value, e = guestTokenSIDAt(data, base, binary.LittleEndian.Uint64(data), header)
		return e
	})
	return
}
func guestPrivilegeLUIDs() (map[uint64]string, error) {
	names := make(map[uint64]string, 3)
	for _, name := range guestPrivilegeNames() {
		text, e := windows.UTF16PtrFromString(name)
		if e != nil {
			return nil, ErrStart
		}
		var value windows.LUID
		if windows.LookupPrivilegeValue(nil, text, &value) != nil {
			return nil, ErrStart
		}
		luid := uint64(uint32(value.HighPart))<<32 | uint64(value.LowPart)
		names[luid] = name
	}
	return names, nil
}
func (windowsKernel) tokenFacts(handle uintptr) (guestTokenFacts, error) {
	token := windows.Token(handle)
	if !ordinaryHandle(handle) {
		return guestTokenFacts{}, ErrStart
	}
	if _, e := tokenType(windows.GetCurrentThreadToken()); e != windows.ERROR_NO_TOKEN {
		return guestTokenFacts{}, ErrStart
	}
	before, err := targetPrincipalOf(token)
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	f := guestTokenFacts{user: before.user, authentication: before.auth, tokenID: before.tokenID, modifiedID: before.modifiedID, kind: 1, session: before.session}
	f.owner, err = guestTokenSID(token, windows.TokenOwner, 8)
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	f.primaryGroup, err = guestTokenSID(token, windows.TokenPrimaryGroup, 8)
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	integrity, err := guestTokenSID(token, windows.TokenIntegrityLevel, 16)
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	switch integrity {
	case "S-1-16-4096":
		f.integrity = 0x1000
	case "S-1-16-8192":
		f.integrity = 0x2000
	default:
		f.integrity = 0xffffffff
	}
	f.elevationType, err = guestTokenDWORD(token, windows.TokenElevationType)
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	for _, field := range []struct {
		class uint32
		out   *bool
	}{{windows.TokenElevation, &f.elevated}, {windows.TokenUIAccess, &f.uiAccess}, {windows.TokenHasRestrictions, &f.restricted}, {guestTokenIsAppContainer, &f.appContainer}} {
		value, e := guestTokenDWORD(token, field.class)
		if e != nil || value > 1 {
			return guestTokenFacts{}, ErrStart
		}
		*field.out = value == 1
	}
	err = withGuestTokenInfo(token, windows.TokenGroups, 65536, func(data []byte, base uint64) error {
		var e error
		f.groups, e = guestTokenGroups(data, base)
		return e
	})
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	names, err := guestPrivilegeLUIDs()
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	err = withGuestTokenInfo(token, windows.TokenPrivileges, 1024, func(data []byte, _ uint64) error {
		var e error
		f.privileges, e = guestTokenPrivileges(data, names)
		return e
	})
	if err != nil {
		return guestTokenFacts{}, ErrStart
	}
	after, err := targetPrincipalOf(token)
	if err != nil || before != after {
		return guestTokenFacts{}, ErrStart
	}
	if _, e := tokenType(windows.GetCurrentThreadToken()); e != windows.ERROR_NO_TOKEN {
		return guestTokenFacts{}, ErrStart
	}
	return f, nil
}
