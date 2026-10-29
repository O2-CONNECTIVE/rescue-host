package guestaccount

import "encoding/binary"

type permissionObjectDescriptor struct {
	owner                                    string
	acl                                      []journalACE
	ownerOffset, aclOffset, aclSize, aclUsed int
	aceOffsets                               []int
}

// SECURITY_DESCRIPTOR_RELATIVE/ACL/ACCESS_ALLOWED_ACEの公開layoutを使うbounds前検査。
// nativeのopaque SDKがpointerを辿る前に全領域を確認し、SDK側でも再照合する。
// https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-dtyp/7d4dac05-9cef-4563-a058-f108abecce1d
// https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-access_allowed_ace
func permissionObjectDescriptorBounds(data []byte) (permissionObjectDescriptor, error) {
	var out permissionObjectDescriptor
	if len(data) < 20 || len(data) > 64*1024 || data[0] != 1 || binary.LittleEndian.Uint16(data[2:])&0x8004 != 0x8004 {
		return out, ErrDenied
	}
	offset := func(pos int, required bool) (int, bool) {
		n := uint64(binary.LittleEndian.Uint32(data[pos:]))
		if n == 0 {
			return 0, !required
		}
		return int(n), n >= 20 && n%4 == 0 && n+8 <= uint64(len(data))
	}
	owner, ok := offset(4, true)
	if !ok {
		return out, ErrDenied
	}
	var err error
	out.owner, err = permissionObjectSID(data, owner, len(data))
	if err != nil {
		return out, ErrDenied
	}
	out.ownerOffset = owner
	if group, ok := offset(8, false); !ok {
		return out, ErrDenied
	} else if group != 0 {
		if _, err := permissionObjectSID(data, group, len(data)); err != nil {
			return out, ErrDenied
		}
	}
	// このAPI要求はOWNER|DACLのみ。SACLの種類別SID layoutへ範囲を広げない。
	if binary.LittleEndian.Uint32(data[12:]) != 0 {
		return out, ErrDenied
	}
	dacl, ok := offset(16, true)
	if !ok {
		return out, ErrDenied
	}
	out.acl, out.aceOffsets, out.aclUsed, err = permissionObjectACLBounds(data, dacl)
	if err != nil {
		return permissionObjectDescriptor{}, err
	}
	out.aclOffset, out.aclSize = dacl, int(binary.LittleEndian.Uint16(data[dacl+2:]))
	return out, nil
}

func permissionObjectSID(data []byte, start, end int) (string, error) {
	if start < 0 || end > len(data) || start+8 > end {
		return "", ErrDenied
	}
	size := 8 + 4*int(data[start+1])
	if start+size > end {
		return "", ErrDenied
	}
	return accountQuerySID(data[start : start+size])
}

func permissionObjectACLBounds(data []byte, start int) ([]journalACE, []int, int, error) {
	if start < 20 || start%4 != 0 || start+8 > len(data) || data[start] != 2 && data[start] != 4 {
		return nil, nil, 0, ErrDenied
	}
	size, count := int(binary.LittleEndian.Uint16(data[start+2:])), int(binary.LittleEndian.Uint16(data[start+4:]))
	if size < 8 || start+size > len(data) || count > 128 {
		return nil, nil, 0, ErrDenied
	}
	aces, offsets := make([]journalACE, 0, count), make([]int, 0, count)
	pos := start + 8
	for i := 0; i < count; i++ {
		if pos+4 > start+size {
			return nil, nil, 0, ErrDenied
		}
		aceSize := int(binary.LittleEndian.Uint16(data[pos+2:]))
		if aceSize < 4 || aceSize%4 != 0 || pos+aceSize > start+size {
			return nil, nil, 0, ErrDenied
		}
		if data[pos] > 1 || aceSize < 16 {
			return nil, nil, 0, ErrDenied
		}
		sid, err := permissionObjectSID(data, pos+8, pos+aceSize)
		if err != nil || 8+8+4*int(data[pos+9]) != aceSize {
			return nil, nil, 0, ErrDenied
		}
		aces = append(aces, journalACE{kind: data[pos], flags: data[pos+1], mask: binary.LittleEndian.Uint32(data[pos+4:]), sid: sid})
		offsets = append(offsets, pos)
		pos += aceSize
	}
	return aces, offsets, pos - start, nil
}
