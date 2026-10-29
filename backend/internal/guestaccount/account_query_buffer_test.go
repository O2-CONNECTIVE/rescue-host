package guestaccount

import (
	"encoding/binary"
	"runtime"
	"testing"
	"unsafe"
)

// USER_INFO_23のSDK layoutから独立に固定したliteral offset/入力。
func accountUser23Bytes(t *testing.T) ([]byte, func(int, uintptr)) {
	t.Helper()
	data := make([]byte, 256)
	pins := &runtime.Pinner{}
	pins.Pin(&data[0])
	t.Cleanup(pins.Unpin)
	putPointer := func(offset int, value uintptr) {
		if unsafe.Sizeof(uintptr(0)) == 8 {
			binary.LittleEndian.PutUint64(data[offset:], uint64(value))
		} else {
			binary.LittleEndian.PutUint32(data[offset:], uint32(value))
		}
	}
	base := uintptr(unsafe.Pointer(&data[0]))
	putPointer(0, base+64)
	for i, c := range "rhg_0123456789abcdef" {
		binary.LittleEndian.PutUint16(data[64+2*i:], uint16(c))
	}
	flagsOffset, sidOffset := 12, 16
	if unsafe.Sizeof(uintptr(0)) == 8 {
		flagsOffset, sidOffset = 24, 32
	}
	binary.LittleEndian.PutUint32(data[flagsOffset:], 0x202)
	putPointer(sidOffset, base+128)
	copy(data[128:], []byte{1, 5, 0, 0, 0, 0, 0, 5})
	for i, v := range []uint32{21, 10, 20, 30, 1002} {
		binary.LittleEndian.PutUint32(data[136+4*i:], v)
	}
	return data, putPointer
}
func TestAccountQueryDecodesBoundedUser23Diagnostic(t *testing.T) {
	data, _ := accountUser23Bytes(t)
	d, err := decodeAccountUser23(&data[0], 256)
	if err != nil || d.kind != accountQueryUser || d.name != "rhg_0123456789abcdef" || d.sid != "S-1-5-21-10-20-30-1002" || d.flags != 0x202 {
		t.Fatalf("SDK診断decode=%+v %v", d, err)
	}
	runtime.KeepAlive(data)
}
func TestAccountQueryRejectsMalformedNativeBuffer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]byte, func(int, uintptr))
		size   uint32
	}{
		{"short-header", func([]byte, func(int, uintptr)) {}, 1},
		{"oversized", func([]byte, func(int, uintptr)) {}, 65537},
		{"nil-name", func(_ []byte, put func(int, uintptr)) { put(0, 0) }, 256},
		{"outside-name", func(d []byte, put func(int, uintptr)) { put(0, uintptr(unsafe.Pointer(&d[0]))+256) }, 256},
		{"unterminated-name", func(d []byte, _ func(int, uintptr)) { binary.LittleEndian.PutUint16(d[104:], 65) }, 256},
		{"unicode-name", func(d []byte, _ func(int, uintptr)) { binary.LittleEndian.PutUint16(d[64:], 0xd800) }, 256},
		{"bad-sid-revision", func(d []byte, _ func(int, uintptr)) { d[128] = 2 }, 256},
		{"sid-too-many-subauthorities", func(d []byte, _ func(int, uintptr)) { d[129] = 16 }, 256},
		{"outside-sid", func(d []byte, put func(int, uintptr)) {
			offset := 16
			if unsafe.Sizeof(uintptr(0)) == 8 {
				offset = 32
			}
			put(offset, uintptr(unsafe.Pointer(&d[0]))+252)
		}, 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, put := accountUser23Bytes(t)
			tc.change(data, put)
			if _, err := decodeAccountUser23(&data[0], tc.size); err == nil {
				t.Fatal("allocation外/不正bufferを受理")
			}
			runtime.KeepAlive(data)
		})
	}
	if _, err := decodeAccountUser23(nil, 256); err == nil {
		t.Fatal("nil buffer受理")
	}
}
