package guestaccount

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

func TestPermissionAliasesBoundedNamesRejectMalformedNativeMemory(t *testing.T) {
	for _, kind := range []string{"nil", "count", "size", "outside", "unaligned", "unpaired", "unterminated", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			api := &permissionAliasesAPIFixture{}
			p := &permissionAliasPage{}
			_ = api.enumerate(p)
			d := api.pages[0]
			switch kind {
			case "nil":
				p.base = nil
			case "count":
				p.entries = 65
			case "size":
				p.size = 65537
			case "outside":
				putPermissionAliasPointer(d, 0, uintptr(unsafe.Pointer(&d[0]))+64)
			case "unaligned":
				putPermissionAliasPointer(d, 0, uintptr(unsafe.Pointer(&d[0]))+17)
			case "unpaired":
				binary.LittleEndian.PutUint16(d[16:], 0xd800)
			case "unterminated":
				for i := 16; i < len(d); i += 2 {
					binary.LittleEndian.PutUint16(d[i:], 65)
				}
			case "duplicate":
				p.entries = 2
				putPermissionAliasPointer(d, int(unsafe.Sizeof(uintptr(0))), uintptr(unsafe.Pointer(&d[0]))+16)
			}
			if _, e := permissionAliasNames(p); e == nil {
				t.Fatal("malformed names accepted")
			}
		})
	}
}
func TestPermissionAliasesBoundedMembersRejectMalformedNativeMemory(t *testing.T) {
	for _, kind := range []string{"nil", "count", "size", "outside", "unaligned", "bad-sid", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			api := &permissionAliasesAPIFixture{memberSID: "S-1-5-32-545"}
			p := &permissionAliasPage{}
			_ = api.members(p)
			d := api.pages[0]
			switch kind {
			case "nil":
				p.base = nil
			case "count":
				p.entries = 257
			case "size":
				p.size = 7
			case "outside":
				putPermissionAliasPointer(d, 0, uintptr(unsafe.Pointer(&d[0]))+124)
			case "unaligned":
				putPermissionAliasPointer(d, 0, uintptr(unsafe.Pointer(&d[0]))+17)
			case "bad-sid":
				d[16] = 2
			case "duplicate":
				p.entries = 2
				putPermissionAliasPointer(d, int(unsafe.Sizeof(uintptr(0))), uintptr(unsafe.Pointer(&d[0]))+16)
			}
			if _, e := permissionAliasMembers(p); e == nil {
				t.Fatal("malformed members accepted")
			}
		})
	}
}

func TestPermissionAliasesLookupLengthIsNotAssumedToBeOutputCharacterCount(t *testing.T) {
	w := [257]uint16{'B', 'U', 'I', 'L', 'T', 'I', 'N', 0}
	for _, reported := range []uint32{7, 8, 257} {
		if got, e := permissionAliasLookupNamespace(w[:], reported); e != nil || got != "BUILTIN" {
			t.Fatal("success capacity convention rejected", reported, got, e)
		}
	}
	if _, e := permissionAliasLookupNamespace(w[:], 258); e == nil {
		t.Fatal("out-of-buffer capacity accepted")
	}
}
