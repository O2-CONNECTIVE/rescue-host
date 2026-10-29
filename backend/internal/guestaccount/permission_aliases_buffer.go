package guestaccount

import (
	"encoding/binary"
	"strconv"
	"strings"
	"unicode/utf16"
	"unsafe"
)

func permissionAliasText(w []uint16) (string, error) {
	if len(w) == 0 || len(w) > 256 {
		return "", ErrDenied
	}
	for i := 0; i < len(w); i++ {
		v := w[i]
		if v == 0 || v < 32 {
			return "", ErrDenied
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+1 >= len(w) || w[i+1] < 0xdc00 || w[i+1] > 0xdfff {
				return "", ErrDenied
			}
			i++
		} else if v >= 0xdc00 && v <= 0xdfff {
			return "", ErrDenied
		}
	}
	s := string(utf16.Decode(w))
	if strings.ContainsAny(s, "\\/\x00") {
		return "", ErrDenied
	}
	return s, nil
}
func permissionAliasNamespace(w []uint16, n uint32) (string, error) {
	if n == 0 || n > 256 || int(n) >= len(w) || w[n] != 0 {
		return "", ErrDenied
	}
	return permissionAliasText(w[:n])
}

// LookupAccountName/Sidのlengthは入力容量でもあり、公式は成功時の
// 文字数更新を保証しない。NUL終端出力を所有buffer内だけで読む。
func permissionAliasLookupNamespace(w []uint16, n uint32) (string, error) {
	if n == 0 || n > uint32(len(w)) {
		return "", ErrDenied
	}
	for i, v := range w {
		if v == 0 {
			return permissionAliasText(w[:i])
		}
		if i == 256 {
			return "", ErrDenied
		}
	}
	return "", ErrDenied
}
func permissionAliasData(p *permissionAliasPage, limit uint32) ([]byte, error) {
	if p.entries > limit || p.size > 64*1024 {
		return nil, ErrDenied
	}
	if p.base == nil {
		if p.entries == 0 && p.size == 0 {
			return []byte{}, nil
		}
		return nil, ErrDenied
	}
	width := uint32(unsafe.Sizeof(uintptr(0)))
	if p.size == 0 || p.size < p.entries*width {
		return nil, ErrDenied
	}
	base := uintptr(unsafe.Pointer(p.base))
	if base+uintptr(p.size) < base {
		return nil, ErrDenied
	}
	return unsafe.Slice(p.base, int(p.size)), nil
}
func permissionAliasNames(p *permissionAliasPage) ([]string, error) {
	d, e := permissionAliasData(p, 64)
	if e != nil {
		return nil, e
	}
	names := make([]string, 0, p.entries)
	for i := uint32(0); i < p.entries; i++ {
		ptr := accountQueryPointer(d, int(i)*int(unsafe.Sizeof(uintptr(0))))
		base := uintptr(unsafe.Pointer(p.base))
		o, ok := accountQueryOffset(ptr, base, len(d), 2)
		if !ok || ptr%2 != 0 || o < int(p.entries)*int(unsafe.Sizeof(uintptr(0))) {
			return nil, ErrDenied
		}
		w := []uint16{}
		ended := false
		for n := 0; n <= 256; n++ {
			if o+2*n+2 > len(d) {
				return nil, ErrDenied
			}
			v := binary.LittleEndian.Uint16(d[o+2*n:])
			if v == 0 {
				ended = true
				break
			}
			w = append(w, v)
		}
		if !ended {
			return nil, ErrDenied
		}
		name, e := permissionAliasText(w)
		if e != nil {
			return nil, e
		}
		for _, old := range names {
			if strings.EqualFold(old, name) {
				return nil, ErrDenied
			}
		}
		names = append(names, name)
	}
	return names, nil
}
func permissionAliasMembers(p *permissionAliasPage) ([]string, error) {
	d, e := permissionAliasData(p, 256)
	if e != nil {
		return nil, e
	}
	out := make([]string, 0, p.entries)
	seen := map[string]bool{}
	for i := uint32(0); i < p.entries; i++ {
		ptr := accountQueryPointer(d, int(i)*int(unsafe.Sizeof(uintptr(0))))
		o, ok := accountQueryOffset(ptr, uintptr(unsafe.Pointer(p.base)), len(d), 8)
		if !ok || ptr%4 != 0 || o < int(p.entries)*int(unsafe.Sizeof(uintptr(0))) {
			return nil, ErrDenied
		}
		sid, e := accountQuerySID(d[o:])
		if e != nil || !permissionSID(sid) || seen[sid] {
			return nil, ErrDenied
		}
		seen[sid] = true
		out = append(out, sid)
	}
	return out, nil
}
func permissionAliasSID(s string, b binding) bool {
	for _, prefix := range []string{b.machine + "-", "S-1-5-32-"} {
		if strings.HasPrefix(s, prefix) {
			part := strings.TrimPrefix(s, prefix)
			n, e := strconv.ParseUint(part, 10, 32)
			return e == nil && strconv.FormatUint(n, 10) == part
		}
	}
	return false
}
func permissionAliasIdentitySID(i *permissionAliasIdentity, m *permissionAliasesMemory) (string, error) {
	if !i.found {
		return "", nil
	}
	if i.use != 4 || i.sidSize < 8 || i.sidSize > 68 || i.sidSize != uint32(8+4*int(i.sid[1])) {
		return "", ErrDenied
	}
	sid, e := accountQuerySID(i.sid[:i.sidSize])
	if e != nil || !permissionAliasSID(sid, m.binding) {
		return "", ErrDenied
	}
	domain, e := permissionAliasLookupNamespace(i.domain[:], i.domainSize)
	if e != nil {
		return "", e
	}
	if strings.HasPrefix(sid, "S-1-5-32-") {
		if !strings.EqualFold(domain, m.builtinName) {
			return "", ErrDenied
		}
	} else if !strings.EqualFold(domain, m.computerName) {
		return "", ErrDenied
	}
	return sid, nil
}
