package serviceready

import (
	"encoding/binary"
	"unsafe"
)

func inspectionPointer(data []byte, offset int) uintptr {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return uintptr(binary.LittleEndian.Uint64(data[offset:]))
	}
	return uintptr(binary.LittleEndian.Uint32(data[offset:]))
}
func inspectionNestedSID(data []byte, offset, min int) (string, error) {
	width := int(unsafe.Sizeof(uintptr(0)))
	if len(data) < offset+width {
		return "", ErrDenied
	}
	ptr := inspectionPointer(data, offset)
	base := uintptr(unsafe.Pointer(&data[0]))
	if ptr < base || ptr-base < uintptr(min) || ptr-base > uintptr(len(data)) || uintptr(len(data))-(ptr-base) < 8 || ptr%4 != 0 {
		return "", ErrDenied
	}
	sid, _, err := inspectionSID(data[int(ptr-base):])
	return sid, err
}
func inspectionTokenUser(data []byte) (string, error) {
	return inspectionNestedSID(data, 0, 2*int(unsafe.Sizeof(uintptr(0))))
}
func inspectionServiceGroup(data []byte, service string) (uint32, error) {
	width := int(unsafe.Sizeof(uintptr(0)))
	header := width
	stride := 2 * width
	if len(data) < header {
		return 0, ErrDenied
	}
	count := int(binary.LittleEndian.Uint32(data))
	if count > 128 || header+count*stride > len(data) {
		return 0, ErrDenied
	}
	found := false
	var attributes uint32
	for i := 0; i < count; i++ {
		offset := header + i*stride
		sid, err := inspectionNestedSID(data, offset, header+count*stride)
		if err != nil {
			return 0, err
		}
		if sid == service {
			if found {
				return 0, ErrDenied
			}
			found = true
			attributes = binary.LittleEndian.Uint32(data[offset+width:])
		}
	}
	if !found {
		return 0, ErrDenied
	}
	return attributes, nil
}
