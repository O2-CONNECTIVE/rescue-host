//go:build windows

package operatorauth

import (
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// nativeThreadは固定した専用helper threadの照会/復帰primitiveであり、
// 保護された認証入口やlauncherを提供しない。
type nativeThread struct {
	owner uint32
}

func (t *nativeThread) lock() {
	if t.owner != 0 {
		t.abort()
	}
	runtime.LockOSThread()
	t.owner = windows.GetCurrentThreadId()
}

func (t *nativeThread) owned() bool {
	return t.owner != 0 && t.owner == windows.GetCurrentThreadId()
}

func (t *nativeThread) unlock() {
	if !t.owned() {
		t.abort()
	}
	t.owner = 0
	runtime.UnlockOSThread()
}

// ERROR_NO_TOKENだけをcleanとする。query-only tokenでも検出時は閉じて
// fatalへ進み、別tokenや別threadで正常化しない。
func (t *nativeThread) clean() error {
	if !t.owned() {
		return ErrFatal
	}
	token, err := nativeOpenThreadToken()
	if token != 0 {
		if token.Close() != nil {
			return ErrFatal
		}
	}
	if err != windows.ERROR_NO_TOKEN || token != 0 {
		return ErrFatal
	}
	return nil
}

var nativeImpersonatePipe = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

func (t *nativeThread) impersonate(pipe uintptr) error {
	if !t.owned() {
		return ErrFatal
	}
	if !nativeRealHandle(pipe) || nativeImpersonatePipe.Find() != nil {
		return ErrDenied
	}
	ok, _, _ := nativeImpersonatePipe.Call(pipe)
	if ok == 0 {
		return ErrDenied
	}
	return nil
}

func (t *nativeThread) token() (out tokenProjection, err error) {
	if !t.owned() {
		return tokenProjection{}, ErrFatal
	}
	token, openErr := nativeOpenThreadToken()
	if token != 0 {
		defer func() {
			if token.Close() != nil {
				out, err = tokenProjection{}, ErrFatal
			}
		}()
	}
	if openErr != nil || token == 0 {
		return tokenProjection{}, ErrDenied
	}
	return nativeProjectToken(token)
}

func nativeOpenThreadToken() (windows.Token, error) {
	var token windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token)
	return token, err
}

func (t *nativeThread) revert() error {
	if !t.owned() || windows.RevertToSelf() != nil {
		return ErrFatal
	}
	return nil
}

// 復帰不能のhelperを再利用可能なcallerへ戻さない。
func (*nativeThread) abort() {
	os.Exit(1)
	panic("operatorauth: process exit returned")
}

func nativeRealHandle(handle uintptr) bool {
	return handle != 0 && handle < ^uintptr(0)-15
}
