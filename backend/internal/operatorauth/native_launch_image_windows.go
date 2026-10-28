//go:build windows

package operatorauth

import (
	"context"
	"crypto/sha256"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (nativeHelperKernel) pinImage(ctx context.Context, s nativeLaunchSpec) (uintptr, error) {
	if !finite(ctx) {
		return 0, ErrDenied
	}
	path, err := windows.UTF16PtrFromString(s.imagePath)
	if err != nil {
		return 0, ErrDenied
	}
	// Noninheritable and no write/delete sharing. The same handle stays pinned
	// through resume and all post-exit binding checks, with no pathname reopen.
	h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if !helperHandle(uintptr(h)) {
			return 0, ErrDenied
		}
		return uintptr(h), ErrDenied
	}
	return uintptr(h), launchCheckImage(ctx, uintptr(h), s)
}
func launchCheckImage(ctx context.Context, handle uintptr, s nativeLaunchSpec) error {
	if !finite(ctx) || !helperHandle(handle) {
		return ErrDenied
	}
	h := windows.Handle(handle)
	var before windows.ByHandleFileInformation
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_DISK || windows.GetFileInformationByHandle(h, &before) != nil || before.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return ErrDenied
	}
	size := uint64(before.FileSizeHigh)<<32 | uint64(before.FileSizeLow)
	if size == 0 || size > uint64(s.maxImageBytes) {
		return ErrDenied
	}
	var path [32768]uint16
	n, err := windows.GetFinalPathNameByHandle(h, &path[0], uint32(len(path)), 0)
	if err != nil || n == 0 || n >= uint32(len(path)) || windows.UTF16ToString(path[:n]) != `\\?\`+s.imagePath {
		return ErrDenied
	}
	var flags uint32
	ok, _, _ := launchGetHandleInfo.Call(handle, uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || flags&windows.HANDLE_FLAG_INHERIT != 0 {
		return ErrDenied
	}
	if _, err = windows.SetFilePointer(h, 0, nil, windows.FILE_BEGIN); err != nil {
		return ErrDenied
	}
	hash := sha256.New()
	var buffer [32768]byte
	var total uint64
	for {
		if !finite(ctx) {
			return ErrDenied
		}
		var count uint32
		err = windows.ReadFile(h, buffer[:], &count, nil)
		if err != nil && err != windows.ERROR_HANDLE_EOF {
			return ErrDenied
		}
		if count > uint32(len(buffer)) {
			return ErrDenied
		}
		total += uint64(count)
		if total > uint64(s.maxImageBytes) || total > size {
			return ErrDenied
		}
		_, _ = hash.Write(buffer[:count])
		if count == 0 || err == windows.ERROR_HANDLE_EOF {
			break
		}
	}
	var actual [32]byte
	copy(actual[:], hash.Sum(nil))
	var after windows.ByHandleFileInformation
	if total != size || actual != s.imageSHA256 || windows.GetFileInformationByHandle(h, &after) != nil || after != before || !finite(ctx) {
		return ErrDenied
	}
	return nil
}
