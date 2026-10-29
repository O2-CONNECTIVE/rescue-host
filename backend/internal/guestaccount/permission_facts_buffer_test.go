package guestaccount

import (
	"encoding/binary"
	"runtime"
	"testing"
	"unsafe"
)

func permissionUser4Bytes(t *testing.T) ([]byte, binding, func(int, uintptr)) {
	t.Helper()
	_, _, b := accountQueryTest(t)
	data := make([]byte, 512)
	pins := &runtime.Pinner{}
	pins.Pin(&data[0])
	t.Cleanup(pins.Unpin)
	put := func(off int, p uintptr) {
		if unsafe.Sizeof(uintptr(0)) == 8 {
			binary.LittleEndian.PutUint64(data[off:], uint64(p))
		} else {
			binary.LittleEndian.PutUint32(data[off:], uint32(p))
		}
	}
	base := uintptr(unsafe.Pointer(&data[0]))
	put(0, base+256)
	for i, c := range b.name {
		binary.LittleEndian.PutUint16(data[256+2*i:], uint16(c))
	}
	flags, sid, primary, priv, auth := 24, 96, 100, 12, 32
	if unsafe.Sizeof(uintptr(0)) == 8 {
		flags, sid, primary, priv, auth = 40, 152, 160, 20, 56
	}
	binary.LittleEndian.PutUint32(data[flags:], 0x202)
	binary.LittleEndian.PutUint32(data[primary:], 777)
	put(sid, base+320)
	binary.LittleEndian.PutUint32(data[priv:], 2)
	binary.LittleEndian.PutUint32(data[auth:], 8)
	encoded, e := permissionFactsSID(b.sid)
	if e != nil {
		t.Fatal(e)
	}
	copy(data[320:], encoded)
	return data, b, put
}

func TestPermissionFactsUser4UsesCompleteSDKLayoutAndBoundIdentity(t *testing.T) {
	wantSize, wantFlags, wantSID, wantPrimary, wantPriv, wantAuth := uintptr(116), uintptr(24), uintptr(96), uintptr(100), uintptr(12), uintptr(32)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantSize, wantFlags, wantSID, wantPrimary, wantPriv, wantAuth = 192, 40, 152, 160, 20, 56
	}
	if unsafe.Sizeof(permissionUser4Layout{}) != wantSize || unsafe.Offsetof(permissionUser4Layout{}.flags) != wantFlags || unsafe.Offsetof(permissionUser4Layout{}.sid) != wantSID || unsafe.Offsetof(permissionUser4Layout{}.primary) != wantPrimary || unsafe.Offsetof(permissionUser4Layout{}.privilege) != wantPriv || unsafe.Offsetof(permissionUser4Layout{}.authFlags) != wantAuth {
		t.Fatal("full USER_INFO_4 SDK ABI mismatch")
	}
	data, b, _ := permissionUser4Bytes(t)
	o, e := decodePermissionUser4(&data[0], uint32(len(data)), b)
	if e != nil || o.kind != accountQueryUser4 || o.name != b.name || o.sid != b.sid || o.primary != 777 || o.flags != 0x202 || o.priv != 2 || o.authFlags != 8 {
		t.Fatal("bound USER_INFO_4 facts missing or defaulted", o, e)
	}
}
