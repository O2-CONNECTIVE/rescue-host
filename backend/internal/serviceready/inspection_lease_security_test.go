package serviceready

import (
	"encoding/binary"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

func inspectionTestSID(s string) []byte {
	p := strings.Split(s, "-")
	out := make([]byte, 8+4*(len(p)-3))
	out[0] = 1
	out[1] = byte(len(p) - 3)
	authority, _ := strconv.ParseUint(p[2], 10, 48)
	for i := 7; i >= 2; i-- {
		out[i] = byte(authority)
		authority >>= 8
	}
	for i, v := range p[3:] {
		n, _ := strconv.ParseUint(v, 10, 32)
		binary.LittleEndian.PutUint32(out[8+4*i:], uint32(n))
	}
	return out
}
func inspectionTestSD() []byte {
	p := listenerSecurity(profile())
	owner := inspectionTestSID(p.OwnerSID)
	out := make([]byte, 20+len(owner))
	out[0] = 1
	binary.LittleEndian.PutUint16(out[2:], 0x9004)
	binary.LittleEndian.PutUint32(out[4:], 20)
	copy(out[20:], owner)
	acl := []byte{2, 0, 0, 0, byte(len(p.ACL)), 0, 0, 0}
	for _, a := range p.ACL {
		sid := inspectionTestSID(a.SID)
		ace := make([]byte, 8+len(sid))
		ace[0] = a.Type
		ace[1] = a.Flags
		binary.LittleEndian.PutUint16(ace[2:], uint16(len(ace)))
		binary.LittleEndian.PutUint32(ace[4:], a.Mask)
		copy(ace[8:], sid)
		acl = append(acl, ace...)
	}
	binary.LittleEndian.PutUint16(acl[2:], uint16(len(acl)))
	binary.LittleEndian.PutUint32(out[16:], uint32(len(out)))
	return append(out, acl...)
}
func TestInspectionLeaseSecurityCodecRejectsUnknownAndTruncatedDescriptors(t *testing.T) {
	data := inspectionTestSD()
	got, err := inspectionSecurity(data)
	if err != nil || !reflect.DeepEqual(got, listenerSecurity(profile())) {
		t.Fatal(got, err)
	}
	for _, kind := range []string{"revision", "absolute", "null-dacl", "owner-outside", "dacl-outside", "count", "unknown-ace", "sid-size", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			d := append([]byte(nil), data...)
			acl := int(binary.LittleEndian.Uint32(d[16:]))
			switch kind {
			case "revision":
				d[0] = 2
			case "absolute":
				d[3] &= 0x7f
			case "null-dacl":
				binary.LittleEndian.PutUint32(d[16:], 0)
			case "owner-outside":
				binary.LittleEndian.PutUint32(d[4:], uint32(len(d)))
			case "dacl-outside":
				binary.LittleEndian.PutUint32(d[16:], uint32(len(d)-1))
			case "count":
				binary.LittleEndian.PutUint16(d[acl+4:], 129)
			case "unknown-ace":
				d[acl+8] = 9
			case "sid-size":
				d[acl+8+8+1] = 15
			case "truncated":
				d = d[:len(d)-1]
			}
			if _, err := inspectionSecurity(d); err == nil {
				t.Fatal("invalid SD accepted")
			}
		})
	}
}
func TestInspectionLeaseTokenSIDAndServiceGroupsStayWithinOwnedBuffer(t *testing.T) {
	width := int(unsafe.Sizeof(uintptr(0)))
	sid := inspectionTestSID(service)
	data := make([]byte, 256)
	pins := &runtime.Pinner{}
	pins.Pin(&data[0])
	defer pins.Unpin()
	put := func(offset int, ptr uintptr) {
		if width == 8 {
			binary.LittleEndian.PutUint64(data[offset:], uint64(ptr))
		} else {
			binary.LittleEndian.PutUint32(data[offset:], uint32(ptr))
		}
	}
	base := uintptr(unsafe.Pointer(&data[0]))
	put(0, base+32)
	copy(data[32:], inspectionTestSID(user))
	if got, e := inspectionTokenUser(data); e != nil || got != user {
		t.Fatal(got, e)
	}
	put(0, base+255)
	if _, e := inspectionTokenUser(data); e == nil {
		t.Fatal("outside token SID accepted")
	}
	clear(data)
	binary.LittleEndian.PutUint32(data, 1)
	put(width, base+32)
	binary.LittleEndian.PutUint32(data[2*width:], 4)
	copy(data[32:], sid)
	if attrs, e := inspectionServiceGroup(data, service); e != nil || attrs != 4 {
		t.Fatal(attrs, e)
	}
	binary.LittleEndian.PutUint32(data, 129)
	if _, e := inspectionServiceGroup(data, service); e == nil {
		t.Fatal("unbounded groups accepted")
	}
	binary.LittleEndian.PutUint32(data, 1)
	put(width, base+254)
	if _, e := inspectionServiceGroup(data, service); e == nil {
		t.Fatal("outside group SID accepted")
	}
}
