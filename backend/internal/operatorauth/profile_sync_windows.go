//go:build windows

package operatorauth

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type winProfileSyncAPI struct{}

var profileSyncDLL = windows.NewLazySystemDLL("kernel32.dll")
var profileSyncCancel = profileSyncDLL.NewProc("CancelSynchronousIo")
var profileSyncReadFile = profileSyncDLL.NewProc("ReadFile")
var profileSyncWriteFile = profileSyncDLL.NewProc("WriteFile")

func nativeProfileSyncAPI() (profileSyncAPI, error) {
	for _, proc := range []*windows.LazyProc{profileSyncCancel, profileSyncReadFile, profileSyncWriteFile} {
		if proc.Find() != nil {
			return nil, ErrUnconfigured
		}
	}
	return winProfileSyncAPI{}, nil
}

func (winProfileSyncAPI) currentThread() (uintptr, error) {
	var thread windows.Handle
	// A real, non-inheritable reference, limited to the right required by
	// CancelSynchronousIo. The worker is already locked to this OS thread.
	err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentThread(), windows.CurrentProcess(), &thread, windows.THREAD_TERMINATE, false, 0)
	return uintptr(thread), err
}

func (winProfileSyncAPI) transfer(kind profileSyncKind, handle uintptr, buffer []byte) (uint32, error) {
	if len(buffer) == 0 {
		return 0, ErrDenied
	}
	proc := profileSyncReadFile
	if kind == profileSyncWrite {
		proc = profileSyncWriteFile
	} else if kind != profileSyncRead {
		return 0, ErrDenied
	}
	var n uint32
	// This is an owned synchronous anonymous-pipe endpoint: OVERLAPPED=NULL,
	// and the count and buffer stay alive until the actual syscall returns.
	result, _, err := proc.Call(handle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&n)), 0)
	runtime.KeepAlive(buffer)
	if result != 0 {
		return n, nil
	}
	if kind == profileSyncRead && errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		return n, errProfileSyncEOF
	}
	return n, err
}

func (winProfileSyncAPI) cancel(thread uintptr) error {
	result, _, err := profileSyncCancel.Call(thread)
	if result != 0 || errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}

func (winProfileSyncAPI) closeThread(thread uintptr) error {
	return windows.CloseHandle(windows.Handle(thread))
}
