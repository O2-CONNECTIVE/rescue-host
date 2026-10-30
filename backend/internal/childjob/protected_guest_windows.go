//go:build windows

package childjob

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Descriptors are supplied at creation, never applied after an unprotected
// object was created. No privilege adjustment or permissive fallback is used.
func protectedAttributes(c *ProtectedGuestConfig, kind protectedKind) (*windows.SecurityAttributes, error) {
	if c == nil {
		return nil, nil
	}
	if !c.valid {
		return nil, ErrConfig
	}
	mask := protectedMask(kind)
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:SYG:SYD:P(A;;0x%x;;;SY)(A;;0x%x;;;%s)", mask, mask, c.service))
	if err != nil || sd == nil {
		return nil, ErrConfig
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

func (windowsKernel) newProtectedJob(c ProtectedGuestConfig) (uintptr, error) {
	attributes, err := protectedAttributes(&c, protectedJob)
	if err != nil || attributes == nil {
		return 0, ErrConfig
	}
	return createOwnedJob(attributes)
}

func (windowsKernel) objectSecurity(h uintptr) (protectedSecurity, error) {
	if !ordinaryHandle(h) {
		return protectedSecurity{}, ErrStart
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(h), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return protectedSecurity{}, ErrStart
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return protectedSecurity{}, ErrStart
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return protectedSecurity{}, ErrStart
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return protectedSecurity{}, ErrStart
	}
	aclSize := binary.LittleEndian.Uint16(unsafe.Slice((*byte)(unsafe.Pointer(acl)), 8)[2:4])
	if aclSize < 8 {
		return protectedSecurity{}, ErrStart
	}
	f := protectedSecurity{owner: owner.String(), present: true, protected: control&windows.SE_DACL_PROTECTED != 0}
	base := uintptr(unsafe.Pointer(acl))
	limit := base + uintptr(aclSize)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var a *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &a) != nil || a == nil {
			return protectedSecurity{}, ErrStart
		}
		address := uintptr(unsafe.Pointer(a))
		if address < base+8 || address > limit || limit-address < 16 || a.Header.AceSize < 16 || uintptr(a.Header.AceSize) > limit-address || a.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || a.Header.AceFlags != 0 {
			return protectedSecurity{}, ErrStart
		}
		sid := (*windows.SID)(unsafe.Pointer(&a.SidStart))
		// Establish the SID's bounded encoded length before any native SID read.
		bytes := unsafe.Slice((*byte)(unsafe.Pointer(&a.SidStart)), int(a.Header.AceSize)-8)
		if len(bytes) < 8 || bytes[0] != 1 || bytes[1] > 15 || 8+4*int(bytes[1]) != len(bytes) || !sid.IsValid() {
			return protectedSecurity{}, ErrStart
		}
		f.entries = append(f.entries, protectedACE{sid: sid.String(), mask: uint32(a.Mask), kind: a.Header.AceType, flags: a.Header.AceFlags})
	}
	return f, nil
}
