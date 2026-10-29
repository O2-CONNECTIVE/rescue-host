package serviceready

import (
	"encoding/binary"
	"strconv"
	"strings"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

func inspectionSID(data []byte) (string, int, error) {
	if len(data) < 8 || data[0] != 1 || data[1] == 0 || data[1] > 15 {
		return "", 0, ErrDenied
	}
	n := 8 + 4*int(data[1])
	if n > len(data) {
		return "", 0, ErrDenied
	}
	authority := uint64(0)
	for _, v := range data[2:8] {
		authority = authority<<8 | uint64(v)
	}
	var out strings.Builder
	out.WriteString("S-1-")
	out.WriteString(strconv.FormatUint(authority, 10))
	for i := 8; i < n; i += 4 {
		out.WriteByte('-')
		out.WriteString(strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[i:])), 10))
	}
	return out.String(), n, nil
}

// GetKernelObjectSecurityのcaller-owned有限bufferを復号する。
// 不明なACL/ACEはnative pointerをたどらず拒否する。
func inspectionSecurity(data []byte) (SecurityPolicy, error) {
	if len(data) < 20 || len(data) > 16384 || data[0] != 1 {
		return SecurityPolicy{}, ErrDenied
	}
	control := binary.LittleEndian.Uint16(data[2:])
	if control&0x8004 != 0x8004 {
		return SecurityPolicy{}, ErrDenied
	}
	owner := uint64(binary.LittleEndian.Uint32(data[4:]))
	dacl := uint64(binary.LittleEndian.Uint32(data[16:]))
	if owner < 20 || owner >= uint64(len(data)) || dacl < 20 || dacl+8 > uint64(len(data)) {
		return SecurityPolicy{}, ErrDenied
	}
	sid, _, err := inspectionSID(data[owner:])
	if err != nil {
		return SecurityPolicy{}, err
	}
	acl := data[dacl:]
	size := int(binary.LittleEndian.Uint16(acl[2:]))
	count := int(binary.LittleEndian.Uint16(acl[4:]))
	if (acl[0] != 2 && acl[0] != 4) || size < 8 || size > len(acl) || count > 128 {
		return SecurityPolicy{}, ErrDenied
	}
	acl = acl[:size]
	out := SecurityPolicy{OwnerSID: sid, Protected: control&0x1000 != 0, ACL: make([]protectedkey.ACE, 0, count)}
	offset := 8
	for i := 0; i < count; i++ {
		if offset+8 > len(acl) {
			return SecurityPolicy{}, ErrDenied
		}
		a := acl[offset:]
		n := int(binary.LittleEndian.Uint16(a[2:]))
		if a[0] > 1 || n < 16 || n > len(a) || n%4 != 0 {
			return SecurityPolicy{}, ErrDenied
		}
		sid, sidSize, err := inspectionSID(a[8:n])
		if err != nil || sidSize != n-8 {
			return SecurityPolicy{}, ErrDenied
		}
		out.ACL = append(out.ACL, protectedkey.ACE{Type: a[0], Flags: a[1], Mask: binary.LittleEndian.Uint32(a[4:]), SID: sid})
		offset += n
	}
	if offset != len(acl) {
		return SecurityPolicy{}, ErrDenied
	}
	return out, nil
}
