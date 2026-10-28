//go:build windows

package protectedkey

import (
	"context"
	"io"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 以下は読取専用 API です。実配置 profile や receiver は接続していません。
// 呼出側は scope 全体を通じ、固定済み thread を所有する必要があります。
func platformScope(ctx context.Context, p Profile) (*NativeScope, error) {
	return newNativeScope(ctx, p, nativeAPI{root: p.Volume.Root})
}

type nativeAPI struct{ root string }

func (nativeAPI) threadID() uint32 { return windows.GetCurrentThreadId() }

func (nativeAPI) principal(context.Context) (principal, error) {
	// query-only pseudo handle の ERROR_NO_TOKEN だけを不存在として受理します。
	// thread query の失敗を process token で代替せず、実 token も取得しません。
	var value, returned uint32
	err := windows.GetTokenInformation(windows.GetCurrentThreadToken(), windows.TokenType, (*byte)(unsafe.Pointer(&value)), 4, &returned)
	if err != windows.ERROR_NO_TOKEN {
		return principal{}, ErrFatal
	}
	token := windows.GetCurrentProcessToken()
	if err := windows.GetTokenInformation(token, windows.TokenType, (*byte)(unsafe.Pointer(&value)), 4, &returned); err != nil || returned != 4 {
		return principal{}, ErrFatal
	}
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return principal{}, ErrFatal
	}
	groups, err := token.GetTokenGroups()
	if err != nil || groups == nil || groups.GroupCount > 1024 {
		return principal{}, ErrFatal
	}
	p := principal{userSID: user.User.Sid.String(), tokenType: value}
	for _, group := range groups.AllGroups() {
		if group.Sid == nil || !group.Sid.IsValid() {
			return principal{}, ErrFatal
		}
		p.groups = append(p.groups, tokenGroup{sid: group.Sid.String(), attributes: group.Attributes})
	}
	runtime.KeepAlive(user)
	runtime.KeepAlive(groups)
	return p, nil
}

func (a nativeAPI) open(ctx context.Context, path string, leaf bool) (protectedFile, error) {
	if !liveDeadline(ctx) {
		return nil, ErrDenied
	}
	root, err := windows.UTF16PtrFromString(a.root)
	if err != nil || windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return nil, ErrDenied
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrDenied
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if leaf {
		access |= windows.FILE_READ_DATA
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil || handle == windows.InvalidHandle || handle == 0 {
		return nil, ErrDenied
	}
	return &nativeFile{handle: handle, thread: windows.GetCurrentThreadId()}, nil
}

type nativeFile struct {
	handle windows.Handle
	thread uint32
}

func (f *nativeFile) inspect() (snapshot, error) {
	if windows.GetCurrentThreadId() != f.thread {
		return snapshot{}, ErrFatal
	}
	kind, err := windows.GetFileType(f.handle)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		return snapshot{}, ErrDenied
	}
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(f.handle, &info) != nil {
		return snapshot{}, ErrDenied
	}
	var identity struct {
		Volume uint64
		ID     [16]byte
	}
	if windows.GetFileInformationByHandleEx(f.handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity))) != nil {
		return snapshot{}, ErrDenied
	}
	path := make([]uint16, 2048)
	n, err := windows.GetFinalPathNameByHandle(f.handle, &path[0], uint32(len(path)), 0)
	if err != nil || n == 0 || n >= uint32(len(path)) {
		return snapshot{}, ErrDenied
	}
	final := windows.UTF16ToString(path[:n])
	if !strings.HasPrefix(final, `\\?\`) {
		return snapshot{}, ErrDenied
	}
	var filesystem [64]uint16
	if windows.GetVolumeInformationByHandle(f.handle, nil, 0, nil, nil, nil, &filesystem[0], uint32(len(filesystem))) != nil {
		return snapshot{}, ErrDenied
	}
	owner, acl, err := inspectSecurity(f.handle)
	if err != nil {
		return snapshot{}, err
	}
	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	if size > uint64(^uint64(0)>>1) {
		return snapshot{}, ErrDenied
	}
	return snapshot{path: strings.TrimPrefix(final, `\\?\`), owner: owner, filesystem: windows.UTF16ToString(filesystem[:]), acl: acl, daclPresent: true, disk: true, directory: info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, reparse: info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, volume: identity.Volume, id: identity.ID, links: info.NumberOfLinks, size: int64(size)}, nil
}

func (f *nativeFile) Read(dst []byte) (int, error) {
	if windows.GetCurrentThreadId() != f.thread {
		return 0, ErrFatal
	}
	var n uint32
	err := windows.ReadFile(f.handle, dst, &n, nil)
	if err == windows.ERROR_HANDLE_EOF || err == nil && n == 0 {
		return int(n), io.EOF
	}
	if err != nil {
		return int(n), ErrDenied
	}
	return int(n), nil
}

func (f *nativeFile) close() error {
	if windows.GetCurrentThreadId() != f.thread {
		return ErrFatal
	}
	if windows.CloseHandle(f.handle) != nil {
		return ErrFatal
	}
	return nil
}

func inspectSecurity(handle windows.Handle) (string, []ACE, error) {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return "", nil, ErrDenied
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return "", nil, ErrDenied
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount > 128 {
		return "", nil, ErrDenied
	}
	entries := make([]ACE, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var native *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &native) != nil || native == nil || native.Header.AceType > 1 || native.Header.AceSize < 16 {
			return "", nil, ErrDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&native.SidStart))
		if !sid.IsValid() || sid.Len() != int(native.Header.AceSize)-8 {
			return "", nil, ErrDenied
		}
		entries = append(entries, ACE{Type: native.Header.AceType, Flags: native.Header.AceFlags, Mask: uint32(native.Mask), SID: sid.String()})
	}
	return owner.String(), entries, nil
}
