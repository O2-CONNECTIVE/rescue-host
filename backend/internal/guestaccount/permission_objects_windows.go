//go:build windows

package guestaccount

import (
	"encoding/binary"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func newNativePermissionObjectReader() (*permissionObjectReader, error) {
	return newPermissionObjectReader(permissionObjectWindows{}), nil
}

type permissionObjectWindows struct{}

var permissionObjectAdvapi = windows.NewLazySystemDLL("advapi32.dll")
var permissionGetKernelSecurity = permissionObjectAdvapi.NewProc("GetKernelObjectSecurity")
var permissionQueryServiceSecurity = permissionObjectAdvapi.NewProc("QueryServiceObjectSecurity")
var permissionGetACLInformation = permissionObjectAdvapi.NewProc("GetAclInformation")

func permissionObjectNativeCall(check func() bool, call func() error) error {
	if !check() {
		return ErrDenied
	}
	err := call()
	if !check() {
		return ErrDenied
	}
	return err
}

// OWNER|DACLだけをcaller-owned bufferへ取得。READ_CONTROL必須、権限を有効化しない。
// https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-getkernelobjectsecurity
// https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-queryserviceobjectsecurity
func (permissionObjectWindows) security(object permissionObject, w *permissionObjectWork, check func() bool) (uint32, error) {
	limit := uint32(len(w.buffer))
	proc := permissionGetKernelSecurity
	if object.kind == permissionObjectService {
		proc, limit = permissionQueryServiceSecurity, 8*1024
	}
	w.needed = 0
	err := permissionObjectNativeCall(check, func() error {
		ok, _, _ := proc.Call(object.handle, uintptr(windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION), uintptr(unsafe.Pointer(&w.buffer[0])), uintptr(limit), uintptr(unsafe.Pointer(&w.needed)))
		if ok == 0 {
			return ErrDenied
		}
		return nil
	})
	if err != nil || w.needed > limit {
		return 0, ErrDenied
	}
	if object.kind == permissionObjectService {
		// service APIのneededは失敗時の記載のみ。成功時は8KiB内をbounds検査しSDK Lengthでも照合。
		return limit, nil
	}
	if w.needed < 20 {
		return 0, ErrDenied
	}
	return w.needed, nil
}

func (permissionObjectWindows) descriptor(data []byte, layout permissionObjectDescriptor, check func() bool) error {
	defer runtime.KeepAlive(data)
	sd := (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(&data[0]))
	var valid bool
	if err := permissionObjectNativeCall(check, func() error {
		valid = sd.IsValid()
		return nil
	}); err != nil || !valid {
		return ErrDenied
	}
	var size uint32
	if err := permissionObjectNativeCall(check, func() error {
		size = sd.Length()
		return nil
	}); err != nil || size < 20 || size > uint32(len(data)) {
		return ErrDenied
	}
	var control windows.SECURITY_DESCRIPTOR_CONTROL
	var revision uint32
	if err := permissionObjectNativeCall(check, func() error {
		var err error
		control, revision, err = sd.Control()
		return err
	}); err != nil || revision != 1 || uint16(control) != binary.LittleEndian.Uint16(data[2:]) {
		return ErrDenied
	}
	base := uintptr(unsafe.Pointer(&data[0]))
	var owner *windows.SID
	if err := permissionObjectNativeCall(check, func() error {
		var err error
		owner, _, err = sd.Owner()
		return err
	}); err != nil || owner == nil || uintptr(unsafe.Pointer(owner)) != base+uintptr(layout.ownerOffset) {
		return ErrDenied
	}
	var acl *windows.ACL
	if err := permissionObjectNativeCall(check, func() error {
		var err error
		acl, _, err = sd.DACL()
		return err
	}); err != nil || acl == nil || uintptr(unsafe.Pointer(acl)) != base+uintptr(layout.aclOffset) {
		return ErrDenied
	}
	// ACLはopaque APIで列挙し、事前に確認した全spanと照合する。
	var info struct{ Count, Used, Free uint32 }
	if err := permissionObjectNativeCall(check, func() error {
		ok, _, _ := permissionGetACLInformation.Call(uintptr(unsafe.Pointer(acl)), uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 2)
		if ok == 0 {
			return ErrDenied
		}
		return nil
	}); err != nil || info.Count != uint32(len(layout.acl)) || info.Used != uint32(layout.aclUsed) || uint64(info.Used)+uint64(info.Free) != uint64(layout.aclSize) {
		return ErrDenied
	}
	for i, offset := range layout.aceOffsets {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := permissionObjectNativeCall(check, func() error {
			return windows.GetAce(acl, uint32(i), &ace)
		}); err != nil || ace == nil || uintptr(unsafe.Pointer(ace)) != base+uintptr(offset) {
			return ErrDenied
		}
		// SDK実return後のscope照合が済んでから、bounds済みACEを読む。
		want := layout.acl[i]
		if ace.Header.AceType != want.kind || ace.Header.AceFlags != want.flags || uint32(ace.Mask) != want.mask {
			return ErrDenied
		}
	}
	return nil
}
