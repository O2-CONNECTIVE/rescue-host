//go:build windows

package operatorauth

import (
	"encoding/binary"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sysのenumにない公式TokenIsAppContainer=29を照会する。
// 予約値TokenIsRestrictedは使わずTokenHasRestrictionsを使う。
const nativeTokenIsAppContainer = 29

func nativeProjectToken(token windows.Token) (tokenProjection, error) {
	kind, err := nativeTokenDWORD(token, windows.TokenType)
	if err != nil || kind != windows.TokenImpersonation {
		return tokenProjection{}, ErrDenied
	}
	level, err := nativeTokenDWORD(token, windows.TokenImpersonationLevel)
	if err != nil || level > windows.SecurityDelegation {
		return tokenProjection{}, ErrDenied
	}
	// TOKEN_STATISTICSは56byte、AuthenticationIdはoffset8のLUID。
	var statsStorage [7]uint64
	stats := unsafe.Slice((*byte)(unsafe.Pointer(&statsStorage[0])), 56)
	var returned uint32
	if windows.GetTokenInformation(token, windows.TokenStatistics, &stats[0], uint32(len(stats)), &returned) != nil || returned != uint32(len(stats)) || binary.LittleEndian.Uint32(stats[24:28]) != kind || binary.LittleEndian.Uint32(stats[28:32]) != level {
		return tokenProjection{}, ErrDenied
	}
	session, err := nativeTokenDWORD(token, windows.TokenSessionId)
	if err != nil {
		return tokenProjection{}, ErrDenied
	}
	restricted, err := nativeTokenDWORD(token, windows.TokenHasRestrictions)
	if err != nil {
		return tokenProjection{}, ErrDenied
	}
	appContainer, err := nativeTokenDWORD(token, nativeTokenIsAppContainer)
	if err != nil || (appContainer == 0 && level <= windows.SecurityIdentification) {
		return tokenProjection{}, ErrDenied
	}
	user, _, _, _, err := nativeTokenSID(token, windows.TokenUser)
	if err != nil {
		return tokenProjection{}, ErrDenied
	}
	_, authority, count, integrity, err := nativeTokenSID(token, windows.TokenIntegrityLevel)
	if err != nil || authority != 16 || count != 1 {
		return tokenProjection{}, ErrDenied
	}
	return tokenProjection{user: user, authentication: binary.LittleEndian.Uint64(stats[8:16]), session: session, integrity: integrity, kind: kind, level: level, restricted: restricted != 0, appContainer: appContainer != 0}, nil
}

func nativeTokenDWORD(token windows.Token, class uint32) (uint32, error) {
	var value, returned uint32
	if windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), 4, &returned) != nil || returned != 4 {
		return 0, ErrDenied
	}
	return value, nil
}

// 固定pin bufferの内側に返却SID全体が収まることを照合する。
// OS返却sizeで再割当や未検査pointerの参照を行わない。
func nativeTokenSID(token windows.Token, class uint32) (string, uint64, byte, uint32, error) {
	var storage [32]uint64
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 256)
	var pins runtime.Pinner
	pins.Pin(&storage[0])
	defer pins.Unpin()
	var returned uint32
	headerSize := uint32(unsafe.Sizeof(windows.SIDAndAttributes{}))
	if windows.GetTokenInformation(token, class, &data[0], uint32(len(data)), &returned) != nil || returned < headerSize || returned > uint32(len(data)) {
		return "", 0, 0, 0, ErrDenied
	}
	header := (*windows.SIDAndAttributes)(unsafe.Pointer(&data[0]))
	base, address := uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(header.Sid))
	if address < base || address%4 != 0 || address-base < uintptr(headerSize) || address-base > uintptr(returned)-8 {
		return "", 0, 0, 0, ErrDenied
	}
	sid := data[address-base : uintptr(returned)]
	count := sid[1]
	if sid[0] != 1 || count > 15 || 8+4*int(count) > len(sid) || !header.Sid.IsValid() {
		return "", 0, 0, 0, ErrDenied
	}
	var authority uint64
	for _, b := range sid[2:8] {
		authority = authority<<8 | uint64(b)
	}
	var last uint32
	if count != 0 {
		last = binary.LittleEndian.Uint32(sid[8+4*(int(count)-1):])
	}
	value := header.Sid.String()
	if value == "" {
		return "", 0, 0, 0, ErrDenied
	}
	return value, authority, count, last, nil
}
