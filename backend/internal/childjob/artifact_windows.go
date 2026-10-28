//go:build windows

package childjob

import (
	"context"
	"crypto/sha256"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (windowsKernel) verifyArtifact(ctx context.Context, artifact Artifact) (uintptr, error) {
	if !localImagePath(artifact.Path) {
		return 0, ErrConfig
	}
	path, err := windows.UTF16PtrFromString(artifact.Path)
	if err != nil {
		return 0, ErrConfig
	}
	// nil SAで非継承。write/delete sharingを許さず、hashからresumeまで同じhandleを保持。
	h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, ErrStart
	}
	info := windows.ByHandleFileInformation{}
	kind, typeErr := windows.GetFileType(h)
	if err := windows.GetFileInformationByHandle(h, &info); err != nil || typeErr != nil || kind != windows.FILE_TYPE_DISK || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return uintptr(h), ErrStart
	}
	hash := sha256.New()
	var buffer [32768]byte
	for {
		if ctx.Err() != nil {
			return uintptr(h), ErrStart
		}
		var count uint32
		err := windows.ReadFile(h, buffer[:], &count, nil)
		if err != nil && err != windows.ERROR_HANDLE_EOF {
			return uintptr(h), ErrStart
		}
		if count != 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if count == 0 || err == windows.ERROR_HANDLE_EOF {
			break
		}
	}
	var actual [32]byte
	copy(actual[:], hash.Sum(nil))
	if actual != artifact.SHA256 || ctx.Err() != nil {
		return uintptr(h), ErrStart
	}
	return uintptr(h), nil
}

func tokenType(token windows.Token) (uint32, error) {
	var kind, returned uint32
	err := windows.GetTokenInformation(token, windows.TokenType, (*byte)(unsafe.Pointer(&kind)), uint32(unsafe.Sizeof(kind)), &returned)
	return kind, err
}
