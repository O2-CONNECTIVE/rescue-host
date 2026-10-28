//go:build windows

package bootstrapauth

import (
	"context"
	"io"
	"runtime"
	"strings"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"golang.org/x/sys/windows"
)

func (windowsKernel) openFile(ctx context.Context, path, root string, leaf bool) (imageFile, error) {
	if !live(ctx) {
		return nil, ErrDenied
	}
	rootPtr, e := windows.UTF16PtrFromString(root)
	if e != nil || windows.GetDriveType(rootPtr) != windows.DRIVE_FIXED {
		return nil, ErrDenied
	}
	ptr, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return nil, ErrDenied
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if leaf {
		access |= windows.FILE_READ_DATA
	}
	h, e := windows.CreateFile(ptr, access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if e != nil || h == 0 || h == windows.InvalidHandle {
		return nil, ErrDenied
	}
	return &nativeFile{handle: h, thread: windows.GetCurrentThreadId()}, nil
}

type nativeFile struct {
	handle windows.Handle
	thread uint32
}

func (f *nativeFile) inspect() (fileState, error) {
	if windows.GetCurrentThreadId() != f.thread {
		return fileState{}, ErrFatal
	}
	kind, e := windows.GetFileType(f.handle)
	if e != nil || kind != windows.FILE_TYPE_DISK {
		return fileState{}, ErrDenied
	}
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(f.handle, &info) != nil {
		return fileState{}, ErrDenied
	}
	var id struct {
		Volume uint64
		ID     [16]byte
	}
	if windows.GetFileInformationByHandleEx(f.handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))) != nil {
		return fileState{}, ErrDenied
	}
	var buf [2048]uint16
	n, e := windows.GetFinalPathNameByHandle(f.handle, &buf[0], uint32(len(buf)), 0)
	if e != nil || n == 0 || n >= uint32(len(buf)) {
		return fileState{}, ErrDenied
	}
	final := windows.UTF16ToString(buf[:n])
	if !strings.HasPrefix(final, `\\?\`) {
		return fileState{}, ErrDenied
	}
	var fs [64]uint16
	if windows.GetVolumeInformationByHandle(f.handle, nil, 0, nil, nil, nil, &fs[0], uint32(len(fs))) != nil {
		return fileState{}, ErrDenied
	}
	owner, acl, _, e := security(f.handle)
	if e != nil {
		return fileState{}, e
	}
	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	if size > uint64(^uint64(0)>>1) {
		return fileState{}, ErrDenied
	}
	return fileState{path: strings.TrimPrefix(final, `\\?\`), owner: owner, acl: acl, filesystem: windows.UTF16ToString(fs[:]), volume: id.Volume, id: id.ID, links: info.NumberOfLinks, size: int64(size), disk: true, directory: info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, reparse: info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, dacl: true}, nil
}
func (f *nativeFile) rewind() error {
	if windows.GetCurrentThreadId() != f.thread {
		return ErrFatal
	}
	var high int32
	n, e := windows.SetFilePointer(f.handle, 0, &high, windows.FILE_BEGIN)
	if e != nil || n != 0 || high != 0 {
		return ErrDenied
	}
	return nil
}
func (f *nativeFile) Read(dst []byte) (int, error) {
	if windows.GetCurrentThreadId() != f.thread {
		return 0, ErrFatal
	}
	var n uint32
	e := windows.ReadFile(f.handle, dst, &n, nil)
	if e == windows.ERROR_HANDLE_EOF || e == nil && n == 0 {
		return int(n), io.EOF
	}
	if e != nil {
		return int(n), ErrDenied
	}
	return int(n), nil
}

// close は取得済み handle の解放専用で、thread の再認証を行いません。
func (f *nativeFile) close() error {
	if windows.CloseHandle(f.handle) != nil {
		return ErrUnconfirmed
	}
	return nil
}
func security(h windows.Handle) (string, []protectedkey.ACE, bool, error) {
	sd, e := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil || sd == nil || !sd.IsValid() {
		return "", nil, false, ErrDenied
	}
	defer runtime.KeepAlive(sd)
	owner, _, e := sd.Owner()
	if e != nil || owner == nil || !owner.IsValid() {
		return "", nil, false, ErrDenied
	}
	control, _, e := sd.Control()
	if e != nil || control&windows.SE_DACL_PRESENT == 0 {
		return "", nil, false, ErrDenied
	}
	acl, _, e := sd.DACL()
	if e != nil || acl == nil || acl.AceCount > 128 {
		return "", nil, false, ErrDenied
	}
	entries := make([]protectedkey.ACE, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var a *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &a) != nil || a == nil || a.Header.AceType > 1 || a.Header.AceSize < 16 {
			return "", nil, false, ErrDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&a.SidStart))
		if sid.Len() != int(a.Header.AceSize)-8 || !sid.IsValid() {
			return "", nil, false, ErrDenied
		}
		entries = append(entries, protectedkey.ACE{Type: a.Header.AceType, Flags: a.Header.AceFlags, Mask: uint32(a.Mask), SID: sid.String()})
	}
	return owner.String(), entries, control&windows.SE_DACL_PROTECTED != 0, nil
}
