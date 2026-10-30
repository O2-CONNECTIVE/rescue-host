//go:build windows

package childjob

import (
	"context"
	"encoding/binary"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformPrepare(ctx context.Context, spec Spec) (*Prepared, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := tokenType(windows.GetCurrentThreadToken()); err != windows.ERROR_NO_TOKEN {
		return nil, ErrConfig
	}
	if kind, err := tokenType(windows.Token(spec.PrimaryToken)); err != nil || kind != windows.TokenPrimary {
		return nil, ErrConfig
	}
	return prepareWithKernel(ctx, spec, windowsKernel{})
}

type targetPrincipal struct {
	user                string
	auth                uint64
	session             uint32
	tokenID, modifiedID uint64
}

// A limited continuity check, not the nonadmin guest policy or logon producer.
// TokenID can differ between source/child; AuthId is only logon binding here.
func targetPrincipalOf(token windows.Token) (targetPrincipal, error) {
	var storage [7]uint64
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 56)
	var size uint32
	if windows.GetTokenInformation(token, windows.TokenStatistics, &data[0], 56, &size) != nil || size != 56 || binary.LittleEndian.Uint32(data[24:28]) != windows.TokenPrimary {
		return targetPrincipal{}, ErrStart
	}
	u, err := token.GetTokenUser()
	if err != nil || u.User.Sid == nil || !u.User.Sid.IsValid() {
		return targetPrincipal{}, ErrStart
	}
	var session uint32
	if windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4, &size) != nil || size != 4 {
		return targetPrincipal{}, ErrStart
	}
	return targetPrincipal{user: u.User.Sid.String(), auth: binary.LittleEndian.Uint64(data[8:16]), session: session, tokenID: binary.LittleEndian.Uint64(data[:8]), modifiedID: binary.LittleEndian.Uint64(data[48:56])}, nil
}

var targetInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
var targetThreadProcess = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

// The returned query handle is retained by Prepared, even on inspection error.
// No PID is accepted from a request or reopened to choose another target.
func (windowsKernel) inspectTarget(spec Spec, process, thread, job uintptr) (uintptr, error) {
	if _, err := tokenType(windows.GetCurrentThreadToken()); err != windows.ERROR_NO_TOKEN {
		return 0, ErrStart
	}
	pid, err := windows.GetProcessId(windows.Handle(process))
	threadPID, _, _ := targetThreadProcess.Call(thread)
	var member uint32
	ok, _, _ := targetInJob.Call(process, job, uintptr(unsafe.Pointer(&member)))
	if err != nil || pid == 0 || uintptr(pid) != threadPID || ok == 0 || member != 1 {
		return 0, ErrStart
	}
	var path [32768]uint16
	size := uint32(len(path))
	if windows.QueryFullProcessImageName(windows.Handle(process), 0, &path[0], &size) != nil || size == 0 || size >= uint32(len(path)) || windows.UTF16ToString(path[:size]) != spec.Artifact.Path {
		return 0, ErrStart
	}
	source := windows.Token(spec.PrimaryToken)
	expected, err := targetPrincipalOf(source)
	if err != nil {
		return 0, err
	}
	var token windows.Token
	access := uint32(windows.TOKEN_QUERY)
	if spec.Protection != nil {
		access |= windows.READ_CONTROL
	}
	if windows.OpenProcessToken(windows.Handle(process), access, &token) != nil {
		return uintptr(token), ErrStart
	}
	actual, err := targetPrincipalOf(token)
	if err != nil || expected.auth == 0 || actual.user != expected.user || actual.auth != expected.auth || actual.session != expected.session {
		return uintptr(token), ErrStart
	}
	afterSource, errSource := targetPrincipalOf(source)
	afterTarget, errTarget := targetPrincipalOf(token)
	if errSource != nil || errTarget != nil || afterSource != expected || afterTarget != actual {
		return uintptr(token), ErrStart
	}
	return uintptr(token), nil
}
