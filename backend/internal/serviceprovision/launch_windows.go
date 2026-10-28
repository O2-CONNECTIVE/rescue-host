//go:build windows

package serviceprovision

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"golang.org/x/sys/windows"
	"unsafe"
)

type launchNativeKernel struct{ ready *serviceready.Factory }

func launchPlatform(ready *serviceready.Factory) launchKernel {
	if ready == nil {
		return nil
	}
	return launchNativeKernel{ready: ready}
}
func (k launchNativeKernel) preflight(ctx context.Context) (launchPreflight, error) {
	p, err := k.ready.PreparePreflight(ctx)
	if p == nil {
		return nil, err
	}
	return p, err
}
func (launchNativeKernel) threadID() uint32   { return windows.GetCurrentThreadId() }
func (launchNativeKernel) currentPID() uint32 { return windows.GetCurrentProcessId() }
func (launchNativeKernel) identity(handle uintptr) (rootPrincipal, error) {
	return (rootNativeKernel{}).principal(handle)
}

type launchNativeToken struct {
	handle  windows.Token
	desired uint32
}

func (launchNativeKernel) openToken(mask uint32) (launchTokenRef, error) {
	if mask != 0x20008 && mask != 0xb {
		return nil, ErrLaunchDenied
	}
	var token windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), mask, &token) != nil || token == 0 {
		return nil, ErrLaunchDenied
	}
	// 新token/credentials/duplicate/adjustを行わず、既存primaryのhandleだけを所有する。
	return &launchNativeToken{handle: token, desired: mask}, nil
}
func (t *launchNativeToken) value() uintptr { return uintptr(t.handle) }
func (t *launchNativeToken) permissions() (uint32, uint32, error) {
	return launchNativePermissions(windows.Handle(t.handle))
}
func (t *launchNativeToken) protect() (launchProtection, error) {
	if t.desired != 0x20008 {
		return launchProtection{}, ErrLaunchDenied
	}
	object, err := launchNativeSecurity(windows.Handle(t.handle))
	if err != nil {
		return launchProtection{}, err
	}
	owner, acl, err := launchNativeDefaults(t.handle)
	if err != nil {
		return launchProtection{}, err
	}
	return launchProtection{object: object, defaultOwner: owner, defaultACL: acl}, nil
}
func (t *launchNativeToken) close() error {
	if t.handle != 0 {
		if t.handle.Close() != nil {
			return ErrLaunchUnconfirmed
		}
		t.handle = 0
	}
	return nil
}

var launchNT = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")

func launchNativePermissions(handle windows.Handle) (uint32, uint32, error) {
	if handle == 0 || launchNT.Find() != nil {
		return 0, 0, ErrLaunchDenied
	}
	// Microsoft公開PUBLIC_OBJECT_BASIC_INFORMATIONのGrantedAccessだけを使う。
	var basic struct {
		Attributes, GrantedAccess, HandleCount, PointerCount uint32
		Reserved                                             [10]uint32
	}
	var returned uint32
	status, _, _ := launchNT.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&basic)), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&returned)))
	if status != 0 || returned != uint32(unsafe.Sizeof(basic)) {
		return 0, 0, ErrLaunchDenied
	}
	flags, err := rootHandleFlags(handle)
	if err != nil {
		return 0, 0, ErrLaunchDenied
	}
	return basic.GrantedAccess, flags, nil
}
