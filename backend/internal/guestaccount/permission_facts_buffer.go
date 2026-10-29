package guestaccount

import (
	"encoding/binary"
	"strconv"
	"strings"
	"unicode/utf16"
	"unsafe"
)

// USER_INFO_4 の全 SDK field。password 等の未使用 pointer は参照しない。
// https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_4
type permissionUser4Layout struct {
	name, password                                           uintptr
	passwordAge, privilege                                   uint32
	home, comment                                            uintptr
	flags                                                    uint32
	script                                                   uintptr
	authFlags                                                uint32
	fullName, userComment, parameters, workstations          uintptr
	lastLogon, lastLogoff, expires, maxStorage, unitsPerWeek uint32
	logonHours                                               uintptr
	badPasswordCount, logons                                 uint32
	logonServer                                              uintptr
	countryCode, codePage                                    uint32
	sid                                                      uintptr
	primary                                                  uint32
	profile, homeDrive                                       uintptr
	passwordExpired                                          uint32
}

// SDK の Win32/Win64 literal layout を compile 時にも固定する。
const permissionFactsPointerBytes = unsafe.Sizeof(uintptr(0))
const permissionUser4Size = 116 + (permissionFactsPointerBytes-4)/4*(192-116)

var _ [unsafe.Sizeof(permissionUser4Layout{}) - permissionUser4Size]byte
var _ [permissionUser4Size - unsafe.Sizeof(permissionUser4Layout{})]byte

func decodePermissionUser4(base *byte, size uint32, b binding) (accountQueryObservation, error) {
	if base == nil || size < uint32(unsafe.Sizeof(permissionUser4Layout{})) || size > 64*1024 {
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
	name := make([]byte, 0, 20)
	terminated := false
	for i := 0; i <= 20; i++ {
		off := nameOffset + 2*i
		if off+2 > len(data) {
			return accountQueryObservation{}, ErrDenied
		}
		v := binary.LittleEndian.Uint16(data[off:])
		if v == 0 {
			terminated = true
			break
		}
		if v > 127 || i == 20 {
			return accountQueryObservation{}, ErrDenied
		}
		name = append(name, byte(v))
	}
	if !terminated || len(name) == 0 || string(name) != b.name {
		return accountQueryObservation{}, ErrDenied
	}
	sidOffset, ok := accountQueryOffset(accountQueryPointer(data, int(unsafe.Offsetof(permissionUser4Layout{}.sid))), address, len(data), 8)
	if !ok {
		return accountQueryObservation{}, ErrDenied
	}
	sid, e := accountQuerySID(data[sidOffset:])
	if e != nil || sid != b.sid {
		return accountQueryObservation{}, ErrDenied
	}
	// name と同じ allocation の SID が一致する前には権限 field を読まない。
	flags := binary.LittleEndian.Uint32(data[int(unsafe.Offsetof(permissionUser4Layout{}.flags)):])
	primary := binary.LittleEndian.Uint32(data[int(unsafe.Offsetof(permissionUser4Layout{}.primary)):])
	priv := binary.LittleEndian.Uint32(data[int(unsafe.Offsetof(permissionUser4Layout{}.privilege)):])
	auth := binary.LittleEndian.Uint32(data[int(unsafe.Offsetof(permissionUser4Layout{}.authFlags)):])
	return accountQueryObservation{kind: accountQueryUser4, name: string(name), sid: sid, flags: flags, primary: primary, priv: priv, authFlags: auth}, nil
}

func permissionFactsRightString(units []uint16) (string, error) {
	if len(units) == 0 || len(units) > 184 {
		return "", ErrDenied
	}
	for i := 0; i < len(units); i++ {
		v := units[i]
		if v == 0 || v == 10 || v == 13 {
			return "", ErrDenied
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", ErrDenied
			}
			i++
		} else if v >= 0xdc00 && v <= 0xdfff {
			return "", ErrDenied
		}
	}
	s := string(utf16.Decode(units))
	if len(s) > 184 {
		return "", ErrDenied
	}
	return s, nil
}

// LSA_UNICODE_STRING は終端 NUL を要求せず、Length の byte 数だけ読む。
// https://learn.microsoft.com/en-us/windows/win32/api/lsalookup/ns-lsalookup-lsa_unicode_string
func permissionFactsRightBuffer(length, maximum uint16, buffer *uint16) (string, error) {
	if length == 0 || length%2 != 0 || length > 368 || maximum < length || maximum%2 != 0 || buffer == nil {
		return "", ErrDenied
	}
	address := uintptr(unsafe.Pointer(buffer))
	if address%2 != 0 || address+uintptr(length) < address {
		return "", ErrDenied
	}
	return permissionFactsRightString(unsafe.Slice(buffer, int(length)/2))
}

// LSA object 未割当は SAM account 不在ではない。他の native error は空にしない。
// https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-lsad/920f27bb-de33-418b-b6a9-add38bcb4f57
func permissionFactsRightsStatus(status, count uint32, data bool) (bool, error) {
	if count > 256 {
		return false, ErrDenied
	}
	if status == 0 {
		if count == 0 {
			return true, nil
		}
		if data {
			return false, nil
		}
		return false, ErrDenied
	}
	if status == 0xc0000034 && count == 0 && !data {
		return true, nil
	}
	return false, ErrDenied
}

// SID文字列以外のname/aliasをLSA入力へ変換しない。token/credentialは作らない。
func permissionFactsSID(s string) ([]byte, error) {
	p := strings.Split(s, "-")
	if len(s) > 184 || len(p) < 4 || len(p) > 18 || p[0] != "S" || p[1] != "1" {
		return nil, ErrDenied
	}
	a, e := strconv.ParseUint(p[2], 10, 48)
	if e != nil || strconv.FormatUint(a, 10) != p[2] {
		return nil, ErrDenied
	}
	b := make([]byte, 8+4*(len(p)-3))
	b[0] = 1
	b[1] = byte(len(p) - 3)
	for i := 7; i >= 2; i-- {
		b[i] = byte(a)
		a >>= 8
	}
	for i, v := range p[3:] {
		n, e := strconv.ParseUint(v, 10, 32)
		if e != nil || strconv.FormatUint(n, 10) != v {
			return nil, ErrDenied
		}
		binary.LittleEndian.PutUint32(b[8+4*i:], uint32(n))
	}
	return b, nil
}
