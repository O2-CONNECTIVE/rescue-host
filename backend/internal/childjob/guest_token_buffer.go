package childjob

import (
	"encoding/binary"
	"strconv"
)

// The candidate Windows targets are amd64/arm64: pointers and SID_AND_ATTRIBUTES
// use 8/16 bytes. No OS-returned pointer is dereferenced before range validation.
func guestTokenSIDAt(data []byte, base, address uint64, minimum int) (string, error) {
	if address < base || address%4 != 0 {
		return "", ErrStart
	}
	offset := address - base
	if offset < uint64(minimum) || len(data) < 8 || offset > uint64(len(data)-8) {
		return "", ErrStart
	}
	sid := data[int(offset):]
	count := int(sid[1])
	if sid[0] != 1 || count > 15 || len(sid) < 8+4*count {
		return "", ErrStart
	}
	authority := uint64(0)
	for _, b := range sid[2:8] {
		authority = authority<<8 | uint64(b)
	}
	value := "S-1-" + strconv.FormatUint(authority, 10)
	for i := 0; i < count; i++ {
		value += "-" + strconv.FormatUint(uint64(binary.LittleEndian.Uint32(sid[8+4*i:])), 10)
	}
	return value, nil
}
func guestTokenGroups(data []byte, base uint64) ([]guestTokenGroup, error) {
	if len(data) < 8 {
		return nil, ErrStart
	}
	count := int(binary.LittleEndian.Uint32(data))
	if count > 64 || len(data) < 8+16*count {
		return nil, ErrStart
	}
	out := make([]guestTokenGroup, 0, count)
	for i := 0; i < count; i++ {
		offset := 8 + 16*i
		sid, err := guestTokenSIDAt(data, base, binary.LittleEndian.Uint64(data[offset:]), 8+16*count)
		if err != nil {
			return nil, err
		}
		out = append(out, guestTokenGroup{sid: sid, attributes: binary.LittleEndian.Uint32(data[offset+8:])})
	}
	return out, nil
}
func guestTokenPrivileges(data []byte, names map[uint64]string) ([]guestTokenPrivilege, error) {
	if len(data) < 4 {
		return nil, ErrStart
	}
	count := int(binary.LittleEndian.Uint32(data))
	if count > 64 || len(data) < 4+12*count {
		return nil, ErrStart
	}
	out := make([]guestTokenPrivilege, 0, count)
	seen := map[uint64]bool{}
	for i := 0; i < count; i++ {
		offset := 4 + 12*i
		luid := binary.LittleEndian.Uint64(data[offset:])
		if seen[luid] {
			return nil, ErrStart
		}
		seen[luid] = true
		out = append(out, guestTokenPrivilege{name: names[luid], luid: luid, attributes: binary.LittleEndian.Uint32(data[offset+8:])})
	}
	return out, nil
}
