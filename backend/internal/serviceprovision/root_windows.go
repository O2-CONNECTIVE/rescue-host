//go:build windows

package serviceprovision

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"golang.org/x/sys/windows"
	"runtime"
	"strings"
	"unsafe"
)

type rootNativeKernel struct{}

func rootPlatform() rootKernel            { return rootNativeKernel{} }
func (rootNativeKernel) threadID() uint32 { return windows.GetCurrentThreadId() }

// OSのqualified lookupだけを使う。service SIDのhash/数値をここで生成しない。
func (rootNativeKernel) serviceSID(name string) (string, error) {
	account, err := windows.UTF16PtrFromString(`NT SERVICE\` + name)
	if err != nil {
		return "", ErrRootDenied
	}
	var sidBytes [68]byte
	var domain [256]uint16
	sidSize, domainSize := uint32(len(sidBytes)), uint32(len(domain))
	var use uint32
	sid := (*windows.SID)(unsafe.Pointer(&sidBytes[0]))
	if windows.LookupAccountName(nil, account, sid, &sidSize, &domain[0], &domainSize, &use) != nil || sidSize > uint32(len(sidBytes)) || !sid.IsValid() || use < 1 || use > 5 || !strings.EqualFold(windows.UTF16ToString(domain[:]), "NT SERVICE") {
		return "", ErrRootDenied
	}
	value := sid.String()
	if !rootServiceIdentifier(value) {
		return "", ErrRootDenied
	}
	var reverse, reverseDomain [256]uint16
	reverseSize, reverseDomainSize := uint32(len(reverse)), uint32(len(reverseDomain))
	if windows.LookupAccountSid(nil, sid, &reverse[0], &reverseSize, &reverseDomain[0], &reverseDomainSize, &use) != nil || use < 1 || use > 5 || !strings.EqualFold(windows.UTF16ToString(reverse[:]), name) || !strings.EqualFold(windows.UTF16ToString(reverseDomain[:]), "NT SERVICE") {
		return "", ErrRootDenied
	}
	return value, nil
}

type rootTokenStats struct {
	TokenID, AuthenticationID                                   windows.LUID
	Expiration                                                  int64
	Kind, Impersonation, Charged, Available, Groups, Privileges uint32
	ModifiedID                                                  windows.LUID
}

func rootLUID(x windows.LUID) uint64 { return uint64(uint32(x.HighPart))<<32 | uint64(x.LowPart) }
func (rootNativeKernel) principal(handle uintptr) (rootPrincipal, error) {
	var kind, returned uint32
	if windows.GetTokenInformation(windows.GetCurrentThreadToken(), windows.TokenType, (*byte)(unsafe.Pointer(&kind)), 4, &returned) != windows.ERROR_NO_TOKEN {
		return rootPrincipal{threadToken: true}, ErrRootFatal
	}
	token := windows.GetCurrentProcessToken()
	if handle != 0 {
		token = windows.Token(handle)
	}
	if windows.GetTokenInformation(token, windows.TokenType, (*byte)(unsafe.Pointer(&kind)), 4, &returned) != nil || returned != 4 || kind != 1 {
		return rootPrincipal{}, ErrRootFatal
	}
	var stats rootTokenStats
	if windows.GetTokenInformation(token, windows.TokenStatistics, (*byte)(unsafe.Pointer(&stats)), uint32(unsafe.Sizeof(stats)), &returned) != nil || returned != uint32(unsafe.Sizeof(stats)) || stats.Kind != kind {
		return rootPrincipal{}, ErrRootFatal
	}
	// token metadataのrealloc retryを行わず、固定上限を越えるqueryは拒否する。
	var userStorage [256]byte
	var groupStorage [64 * 1024]byte
	var pins runtime.Pinner
	pins.Pin(&userStorage[0])
	pins.Pin(&groupStorage[0])
	defer pins.Unpin()
	if windows.GetTokenInformation(token, windows.TokenUser, &userStorage[0], uint32(len(userStorage)), &returned) != nil || returned < uint32(unsafe.Sizeof(windows.Tokenuser{})) || returned > uint32(len(userStorage)) {
		return rootPrincipal{}, ErrRootFatal
	}
	user := (*windows.Tokenuser)(unsafe.Pointer(&userStorage[0]))
	if !rootSIDIn(user.User.Sid, userStorage[:returned]) {
		return rootPrincipal{}, ErrRootFatal
	}
	userSID := user.User.Sid.String()
	if !rootSID(userSID) {
		return rootPrincipal{}, ErrRootFatal
	}
	result := rootPrincipal{user: userSID, kind: kind, tokenID: rootLUID(stats.TokenID), authenticationID: rootLUID(stats.AuthenticationID), modifiedID: rootLUID(stats.ModifiedID)}
	if windows.GetTokenInformation(token, windows.TokenGroups, &groupStorage[0], uint32(len(groupStorage)), &returned) != nil || returned < 4 || returned > uint32(len(groupStorage)) {
		return rootPrincipal{}, ErrRootFatal
	}
	groups := (*windows.Tokengroups)(unsafe.Pointer(&groupStorage[0]))
	offset := unsafe.Offsetof(groups.Groups)
	if groups.GroupCount > 1024 || offset+uintptr(groups.GroupCount)*unsafe.Sizeof(windows.SIDAndAttributes{}) > uintptr(returned) {
		return rootPrincipal{}, ErrRootFatal
	}
	for _, g := range groups.AllGroups() {
		if !rootSIDIn(g.Sid, groupStorage[:returned]) {
			return rootPrincipal{}, ErrRootFatal
		}
		groupSID := g.Sid.String()
		if !rootSID(groupSID) {
			return rootPrincipal{}, ErrRootFatal
		}
		result.groups = append(result.groups, rootGroup{sid: groupSID, attributes: g.Attributes})
	}
	return result, nil
}
func rootSIDIn(sid *windows.SID, buffer []byte) bool {
	if sid == nil || len(buffer) < 8 {
		return false
	}
	start, base := uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&buffer[0]))
	end := base + uintptr(len(buffer))
	if start < base || start+8 > end {
		return false
	}
	size := uintptr(sid.Len())
	return size >= 8 && size <= 68 && start+size <= end && sid.IsValid()
}

type rootNativeToken struct{ handle windows.Token }

func (rootNativeKernel) openQueryToken() (rootToken, error) {
	var token windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token) != nil || token == 0 {
		return nil, ErrRootDenied
	}
	out := &rootNativeToken{handle: token}
	if flags, err := rootHandleFlags(windows.Handle(token)); err != nil || flags != 0 {
		return out, ErrRootDenied
	}
	return out, nil
}
func (t *rootNativeToken) value() uintptr { return uintptr(t.handle) }
func (t *rootNativeToken) close() error {
	if t.handle != 0 {
		if t.handle.Close() != nil {
			return ErrRootUnconfirmed
		}
		t.handle = 0
	}
	return nil
}

type rootNativeFile struct {
	handle windows.Handle
	thread uint32
}

func (k rootNativeKernel) open(path string, leaf bool) (rootFile, error) {
	// driveは検証済みpathの固定root。実volumeは全held handleでconfigured値と照合する。
	drive, err := windows.UTF16PtrFromString(path[:3])
	if err != nil || windows.GetDriveType(drive) != windows.DRIVE_FIXED {
		return nil, ErrRootDenied
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrRootDenied
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if leaf {
		access |= windows.FILE_READ_DATA
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil || handle == 0 || handle == windows.InvalidHandle {
		return nil, ErrRootDenied
	}
	return &rootNativeFile{handle: handle, thread: windows.GetCurrentThreadId()}, nil
}
func (f *rootNativeFile) inspect() (rootFileState, error) {
	if windows.GetCurrentThreadId() != f.thread {
		return rootFileState{}, ErrRootFatal
	}
	kind, err := windows.GetFileType(f.handle)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		return rootFileState{}, ErrRootDenied
	}
	flags, err := rootHandleFlags(f.handle)
	if err != nil || flags != 0 {
		return rootFileState{}, ErrRootDenied
	}
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(f.handle, &info) != nil {
		return rootFileState{}, ErrRootDenied
	}
	var identity struct {
		Volume uint64
		ID     [16]byte
	}
	if windows.GetFileInformationByHandleEx(f.handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity))) != nil {
		return rootFileState{}, ErrRootDenied
	}
	var path [2048]uint16
	n, err := windows.GetFinalPathNameByHandle(f.handle, &path[0], uint32(len(path)), 0)
	if err != nil || n == 0 || n >= uint32(len(path)) {
		return rootFileState{}, ErrRootDenied
	}
	final := windows.UTF16ToString(path[:n])
	if !strings.HasPrefix(final, `\\?\`) {
		return rootFileState{}, ErrRootDenied
	}
	var filesystem [64]uint16
	if windows.GetVolumeInformationByHandle(f.handle, nil, 0, nil, nil, nil, &filesystem[0], uint32(len(filesystem))) != nil {
		return rootFileState{}, ErrRootDenied
	}
	owner, acl, protected, err := rootNativeSecurity(f.handle)
	if err != nil {
		return rootFileState{}, err
	}
	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	if size > uint64(^uint64(0)>>1) {
		return rootFileState{}, ErrRootDenied
	}
	return rootFileState{path: strings.TrimPrefix(final, `\\?\`), owner: owner, filesystem: windows.UTF16ToString(filesystem[:]), volume: identity.Volume, id: identity.ID, acl: acl, dacl: true, protected: protected, disk: true, directory: info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, reparse: info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, links: info.NumberOfLinks, size: int64(size)}, nil
}
func (f *rootNativeFile) close() error {
	if f.handle != 0 {
		if windows.CloseHandle(f.handle) != nil {
			return ErrRootUnconfirmed
		}
		f.handle = 0
	}
	return nil
}
func rootNativeSecurity(handle windows.Handle) (string, []protectedkey.ACE, bool, error) {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return "", nil, false, ErrRootDenied
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return "", nil, false, ErrRootDenied
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return "", nil, false, ErrRootDenied
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount > 128 {
		return "", nil, false, ErrRootDenied
	}
	entries := make([]protectedkey.ACE, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil || ace.Header.AceType > 1 || ace.Header.AceSize < 16 {
			return "", nil, false, ErrRootDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sid.Len() != int(ace.Header.AceSize)-8 {
			return "", nil, false, ErrRootDenied
		}
		entries = append(entries, protectedkey.ACE{Type: ace.Header.AceType, Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask), SID: sid.String()})
	}
	return owner.String(), entries, control&windows.SE_DACL_PROTECTED != 0, nil
}
