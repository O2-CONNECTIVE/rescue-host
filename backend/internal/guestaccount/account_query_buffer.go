package guestaccount

import (
	"encoding/binary"
	"strconv"
	"strings"
	"unsafe"
)

// USER_INFO_23: https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_23
// fullname/commentは不要なので参照しない。返却allocation外のnested pointerは拒否。
type accountUser23Layout struct {
	name, fullName, comment uintptr
	flags                   uint32
	sid                     uintptr
}

func decodeAccountUser23(base *byte, size uint32) (accountQueryObservation, error) {
	if base == nil || size < uint32(unsafe.Sizeof(accountUser23Layout{})) || size > 64*1024 {
		return accountQueryObservation{}, ErrDenied
	}
	address := uintptr(unsafe.Pointer(base))
	if address+uintptr(size) < address {
		return accountQueryObservation{}, ErrDenied
	}
	data := unsafe.Slice(base, int(size))
	nameOffset, ok := accountQueryOffset(accountQueryPointer(data, 0), address, len(data), 2)
	if !ok {
		return accountQueryObservation{}, ErrDenied
	}
	name := ""
	for i := 0; i <= 20; i++ {
		offset := nameOffset + 2*i
		if offset+2 > len(data) {
			return accountQueryObservation{}, ErrDenied
		}
		value := binary.LittleEndian.Uint16(data[offset:])
		if value == 0 {
			break
		}
		// 生成名はASCII。Unicode正規化や置換文字による別名採用はしない。
		if value > 127 || i == 20 {
			return accountQueryObservation{}, ErrDenied
		}
		name += string(rune(value))
	}
	if name == "" {
		return accountQueryObservation{}, ErrDenied
	}
	sidOffset, ok := accountQueryOffset(accountQueryPointer(data, int(unsafe.Offsetof(accountUser23Layout{}.sid))), address, len(data), 8)
	if !ok {
		return accountQueryObservation{}, ErrDenied
	}
	sid, err := accountQuerySID(data[sidOffset:])
	if err != nil {
		return accountQueryObservation{}, err
	}
	flags := binary.LittleEndian.Uint32(data[int(unsafe.Offsetof(accountUser23Layout{}.flags)):])
	return accountQueryObservation{kind: accountQueryUser, name: name, sid: sid, flags: flags}, nil
}
func accountQueryPointer(data []byte, offset int) uintptr {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return uintptr(binary.LittleEndian.Uint64(data[offset:]))
	}
	return uintptr(binary.LittleEndian.Uint32(data[offset:]))
}
func accountQueryOffset(pointer, base uintptr, length, need int) (int, bool) {
	if pointer < base {
		return 0, false
	}
	offset := pointer - base
	if offset > uintptr(length) || uintptr(need) > uintptr(length)-offset {
		return 0, false
	}
	return int(offset), true
}
func accountQuerySID(data []byte) (string, error) {
	if len(data) < 8 || data[0] != 1 || data[1] > 15 || len(data) < 8+4*int(data[1]) {
		return "", ErrDenied
	}
	authority := uint64(0)
	for _, b := range data[2:8] {
		authority = authority<<8 | uint64(b)
	}
	var out strings.Builder
	out.WriteString("S-1-")
	out.WriteString(strconv.FormatUint(authority, 10))
	for i := 0; i < int(data[1]); i++ {
		out.WriteByte('-')
		out.WriteString(strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[8+4*i:])), 10))
	}
	return out.String(), nil
}
