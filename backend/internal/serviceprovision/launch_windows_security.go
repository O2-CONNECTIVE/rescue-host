//go:build windows

package serviceprovision

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

var launchGetKernelSecurity = windows.NewLazySystemDLL("advapi32.dll").NewProc("GetKernelObjectSecurity")
var launchGetACLInformation = windows.NewLazySystemDLL("advapi32.dll").NewProc("GetAclInformation")

func launchNativeSecurity(handle windows.Handle) (launchObjectSecurity, error) {
	if launchGetKernelSecurity.Find() != nil {
		return launchObjectSecurity{}, ErrLaunchDenied
	}
	buffer := make([]byte, 64*1024)
	var pins runtime.Pinner
	pins.Pin(&buffer[0])
	defer pins.Unpin()
	var needed uint32
	result, _, _ := launchGetKernelSecurity.Call(uintptr(handle), uintptr(windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&needed)))
	if result == 0 || needed < 20 || needed > uint32(len(buffer)) {
		return launchObjectSecurity{}, ErrLaunchDenied
	}
	buffer = buffer[:needed]
	sd := (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(&buffer[0]))
	if !sd.IsValid() {
		return launchObjectSecurity{}, ErrLaunchDenied
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_SELF_RELATIVE == 0 {
		return launchObjectSecurity{}, ErrLaunchDenied
	}
	owner, _, err := sd.Owner()
	if err != nil || !rootSIDIn(owner, buffer) {
		return launchObjectSecurity{}, ErrLaunchDenied
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return launchObjectSecurity{}, ErrLaunchDenied
	}
	entries, err := launchReadACL(acl, buffer)
	if err != nil {
		return launchObjectSecurity{}, err
	}
	return launchObjectSecurity{owner: owner.String(), acl: entries}, nil
}
func launchReadACL(acl *windows.ACL, buffer []byte) ([]protectedkey.ACE, error) {
	if acl == nil || len(buffer) < 8 {
		return nil, ErrLaunchDenied
	}
	begin, limit := uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&buffer[0]))+uintptr(len(buffer))
	address := uintptr(unsafe.Pointer(acl))
	if address < begin || address+unsafe.Sizeof(*acl) > limit || launchGetACLInformation.Find() != nil {
		return nil, ErrLaunchDenied
	}
	// ACLはopaqueとして扱い、公式ACL_SIZE_INFORMATIONで領域と件数を得る。
	var information struct{ Count, Used, Free uint32 }
	result, _, _ := launchGetACLInformation.Call(address, uintptr(unsafe.Pointer(&information)), unsafe.Sizeof(information), 2)
	if result == 0 || information.Count > 128 || information.Used < 8 || uint64(information.Used)+uint64(information.Free) > uint64(limit-address) {
		return nil, ErrLaunchDenied
	}
	end := address + uintptr(information.Used)
	entries := make([]protectedkey.ACE, 0, information.Count)
	for i := uint32(0); i < information.Count; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil {
			return nil, ErrLaunchDenied
		}
		pos := uintptr(unsafe.Pointer(ace))
		if pos < address+8 || pos+8 > end || ace.Header.AceSize < 16 || pos+uintptr(ace.Header.AceSize) > end || ace.Header.AceType > 1 {
			return nil, ErrLaunchDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !rootSIDIn(sid, buffer) || sid.Len() != int(ace.Header.AceSize)-8 {
			return nil, ErrLaunchDenied
		}
		entries = append(entries, protectedkey.ACE{Type: ace.Header.AceType, Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask), SID: sid.String()})
	}
	return entries, nil
}
func launchNativeDefaults(token windows.Token) (string, []protectedkey.ACE, error) {
	ownerBuffer := make([]byte, 256)
	defaultBuffer := make([]byte, 64*1024)
	var pins runtime.Pinner
	pins.Pin(&ownerBuffer[0])
	pins.Pin(&defaultBuffer[0])
	defer pins.Unpin()
	var needed uint32
	if windows.GetTokenInformation(token, windows.TokenOwner, &ownerBuffer[0], uint32(len(ownerBuffer)), &needed) != nil || needed < uint32(unsafe.Sizeof(uintptr(0))) || needed > uint32(len(ownerBuffer)) {
		return "", nil, ErrLaunchDenied
	}
	owner := (*struct{ SID *windows.SID })(unsafe.Pointer(&ownerBuffer[0])).SID
	if !rootSIDIn(owner, ownerBuffer[:needed]) {
		return "", nil, ErrLaunchDenied
	}
	ownerSID := owner.String()
	if windows.GetTokenInformation(token, windows.TokenDefaultDacl, &defaultBuffer[0], uint32(len(defaultBuffer)), &needed) != nil || needed < uint32(unsafe.Sizeof(uintptr(0))) || needed > uint32(len(defaultBuffer)) {
		return "", nil, ErrLaunchDenied
	}
	acl := (*struct{ ACL *windows.ACL })(unsafe.Pointer(&defaultBuffer[0])).ACL
	entries, err := launchReadACL(acl, defaultBuffer[:needed])
	if err != nil {
		return "", nil, err
	}
	return ownerSID, entries, nil
}
