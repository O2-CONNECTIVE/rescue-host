//go:build windows

package localcontrol

import (
	"context"
	"os"
	"runtime"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"golang.org/x/sys/windows"
)

var impersonatePipe = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

type nativeAPI struct{}

// accept/overlapped readとshared codecの結合は別の未実装境界。
func (nativeAPI) read(context.Context, uintptr) (Command, error) { return Command{}, ErrClosed }

// NewNativeはAPIを構成するだけでpipeを作らない。Openの製品gateは閉鎖中。
func NewNative(config Config, coordinator *control.Coordinator) (*Boundary, error) {
	if coordinator == nil {
		return nil, ErrConfig
	}
	return newBoundary(config, nativeAPI{}, coordinator)
}

func (nativeAPI) create(plan PipePlan) (uintptr, error) {
	name, err := windows.UTF16PtrFromString(plan.Name)
	if err != nil {
		return 0, ErrConfig
	}
	sd, err := windows.SecurityDescriptorFromString(plan.SDDL)
	if err != nil {
		return 0, ErrConfig
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd, InheritHandle: 0}
	handle, err := windows.CreateNamedPipe(name, plan.OpenMode, plan.PipeMode, plan.MaxInstances, plan.BufferBytes, plan.BufferBytes, 0, &attributes)
	runtime.KeepAlive(sd)
	if err != nil {
		return 0, ErrDenied
	}
	return uintptr(handle), nil
}
func (nativeAPI) close(handle uintptr) error { return windows.CloseHandle(windows.Handle(handle)) }
func (nativeAPI) inspect(handle uintptr) error {
	var flags, instances uint32
	if err := windows.GetNamedPipeInfo(windows.Handle(handle), &flags, nil, nil, &instances); err != nil {
		return ErrDenied
	}
	if flags&windows.PIPE_SERVER_END == 0 || flags&windows.PIPE_TYPE_MESSAGE == 0 || instances != 1 {
		return ErrDenied
	}
	// ACL/remote拒否の証明には使わない。handleは上のcreateが所有したものだけ。
	return nil
}
func (nativeAPI) serverSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", ErrDenied
	}
	return user.User.Sid.String(), nil
}
func (nativeAPI) pin()   { runtime.LockOSThread() }
func (nativeAPI) unpin() { runtime.UnlockOSThread() }
func (nativeAPI) hasThreadToken() (bool, error) {
	thread, err := windows.GetCurrentThread()
	if err != nil {
		return false, ErrFatal
	}
	var token windows.Token
	err = windows.OpenThreadToken(thread, windows.TOKEN_QUERY, true, &token)
	if err == windows.ERROR_NO_TOKEN {
		return false, nil
	}
	if err != nil {
		return false, ErrFatal
	}
	if err = token.Close(); err != nil {
		return true, ErrFatal
	}
	return true, nil
}
func (nativeAPI) impersonate(handle uintptr) error {
	result, _, _ := impersonatePipe.Call(handle)
	if result == 0 {
		return ErrDenied
	}
	return nil
}
func (nativeAPI) clientToken() (peer peerToken, result error) {
	thread, err := windows.GetCurrentThread()
	if err != nil {
		return peer, ErrDenied
	}
	var token windows.Token
	// process tokenへfallbackしない。OpenAsSelfはtokenを開くaccess checkにだけ使う。
	if err = windows.OpenThreadToken(thread, windows.TOKEN_QUERY, true, &token); err != nil {
		return peer, ErrDenied
	}
	defer func() {
		if token.Close() != nil {
			result = ErrFatal
		}
	}()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return peer, ErrDenied
	}
	typeValue, err := tokenWord(token, windows.TokenType)
	if err != nil {
		return peer, err
	}
	level, err := tokenWord(token, windows.TokenImpersonationLevel)
	if err != nil {
		return peer, err
	}
	return peerToken{sid: user.User.Sid.String(), tokenType: typeValue, level: level}, nil
}
func tokenWord(token windows.Token, class uint32) (uint32, error) {
	var value, length uint32
	if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &length); err != nil || length != uint32(unsafe.Sizeof(value)) {
		return 0, ErrDenied
	}
	return value, nil
}
func (nativeAPI) revert() error { return windows.RevertToSelf() }
func (nativeAPI) fatal()        { os.Exit(1) }
