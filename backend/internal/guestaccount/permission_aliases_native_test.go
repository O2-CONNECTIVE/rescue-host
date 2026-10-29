package guestaccount

import (
	"encoding/binary"
	"errors"
	"testing"
	"unsafe"
)

type permissionAliasesAPIFixture struct {
	trace                     []string
	pages                     [][]byte
	more, freeFail, panicEnum bool
	memberSID                 string
}

func (k *permissionAliasesAPIFixture) computer(m *permissionAliasesMemory) error {
	k.trace = append(k.trace, "computer")
	copy(m.computerBuffer[:], []uint16{'H', 'O', 'S', 'T'})
	m.computerSize = 4
	return nil
}
func (k *permissionAliasesAPIFixture) builtin(m *permissionAliasesMemory) error {
	k.trace = append(k.trace, "builtin")
	copy(m.builtinBuffer[:], []uint16{'B', 'U', 'I', 'L', 'T', 'I', 'N', 0})
	m.builtinSize = 7
	m.builtinDomainSize = 0
	m.builtinUse = 3
	return nil
}
func (k *permissionAliasesAPIFixture) enumerate(p *permissionAliasPage) error {
	k.trace = append(k.trace, "enum")
	if k.panicEnum {
		panic("partial out")
	}
	d := make([]byte, 64)
	wide := []uint16{'U', 's', 'e', 'r', 's', 0}
	base := uintptr(unsafe.Pointer(&d[0]))
	putPermissionAliasPointer(d, 0, base+16)
	for i, v := range wide {
		binary.LittleEndian.PutUint16(d[16+2*i:], v)
	}
	k.pages = append(k.pages, d)
	p.base = &d[0]
	p.entries = 1
	p.total = 987
	p.size = 64
	p.status = 0
	if k.more {
		p.status = 234
		p.resume = 42
	}
	return nil
}
func (k *permissionAliasesAPIFixture) lookup(i *permissionAliasIdentity) error {
	k.trace = append(k.trace, "lookup")
	s := permissionAliasUTF16Test(i.qualified)
	if s == "HOST\\Users" {
		i.found = false
		return nil
	}
	if s != "BUILTIN\\Users" {
		return ErrDenied
	}
	d, _ := permissionFactsSID("S-1-5-32-545")
	copy(i.sid[:], d)
	i.sidSize = uint32(len(d))
	copy(i.domain[:], []uint16{'B', 'U', 'I', 'L', 'T', 'I', 'N'})
	i.domainSize = 7
	i.use = 4
	i.found = true
	return nil
}
func (k *permissionAliasesAPIFixture) members(p *permissionAliasPage) error {
	k.trace = append(k.trace, "members")
	sid, _ := permissionFactsSID(k.memberSID)
	d := make([]byte, 128)
	putPermissionAliasPointer(d, 0, uintptr(unsafe.Pointer(&d[0]))+16)
	copy(d[16:], sid)
	k.pages = append(k.pages, d)
	p.base = &d[0]
	p.entries = 1
	p.total = 1
	p.size = 128
	p.status = 0
	return nil
}
func (k *permissionAliasesAPIFixture) size(*permissionAliasPage) error {
	k.trace = append(k.trace, "size")
	return nil
}
func (k *permissionAliasesAPIFixture) free(*permissionAliasPage) error {
	k.trace = append(k.trace, "free")
	if k.freeFail {
		return errors.New("free failed")
	}
	return nil
}
func putPermissionAliasPointer(d []byte, o int, p uintptr) {
	if unsafe.Sizeof(p) == 8 {
		binary.LittleEndian.PutUint64(d[o:], uint64(p))
	} else {
		binary.LittleEndian.PutUint32(d[o:], uint32(p))
	}
}
func permissionAliasUTF16Test(v []uint16) string {
	b := make([]rune, 0, len(v))
	for _, x := range v {
		if x == 0 {
			break
		}
		b = append(b, rune(x))
	}
	return string(b)
}

func TestPermissionAliasesNativeSnapshotUsesPublicCallsAndIgnoresTotalHint(t *testing.T) {
	_, _, b := accountQueryTest(t)
	api := &permissionAliasesAPIFixture{memberSID: b.sid}
	k := &permissionAliasesKernel{api: api}
	m, e := k.snapshot(testContext(t), b, func() bool { return true })
	if e != nil {
		t.Fatal("complete native snapshot rejected", e)
	}
	o, e := m.decode()
	if e != nil || len(o.groups) != 1 || o.groups[0].sid != "S-1-5-32-545" || o.groups[0].members[0] != b.sid {
		t.Fatal(o, e)
	}
	if e = m.release(func() bool { return true }); e != nil {
		t.Fatal(e)
	}
}
func TestPermissionAliasesMoreDataRetainsContinuationAfterFree(t *testing.T) {
	_, _, b := accountQueryTest(t)
	api := &permissionAliasesAPIFixture{more: true}
	k := &permissionAliasesKernel{api: api}
	m, e := k.snapshot(testContext(t), b, func() bool { return true })
	if e == nil || m == nil {
		t.Fatal("paging became complete")
	}
	if _, e = m.decode(); e == nil {
		t.Fatal("partial decoded")
	}
	if e = m.release(func() bool { return true }); e != ErrCleanup {
		t.Fatal("RPC continuation became cleanup ack", e)
	}
	p := m.(*permissionAliasesMemory)
	if p.pages[0].base != nil || p.pages[0].resume != 42 || !p.rpcPending {
		t.Fatal("buffer/continuation conflated")
	}
}
