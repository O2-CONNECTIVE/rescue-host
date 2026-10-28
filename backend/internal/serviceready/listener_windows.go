//go:build windows

package serviceready

import (
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

var readyDLL = windows.NewLazySystemDLL("kernel32.dll")
var getHandleInformation = readyDLL.NewProc("GetHandleInformation")

type nativeListener struct {
	handle windows.Handle
	thread uint32
}

func (nativeKernel) createListener(endpoint, descriptor string) (listenerRef, error) {
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		return nil, ErrDenied
	}
	path, err := windows.UTF16PtrFromString(endpoint)
	if err != nil {
		return nil, ErrDenied
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd, InheritHandle: 0}
	handle, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 16384, 16384, 0, &sa)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, ErrDenied
	}
	return &nativeListener{handle: handle, thread: windows.GetCurrentThreadId()}, nil
}
func (l *nativeListener) inspect() (listenerState, error) {
	if windows.GetCurrentThreadId() != l.thread {
		return listenerState{}, ErrFatal
	}
	kind, err := windows.GetFileType(l.handle)
	if err != nil {
		return listenerState{}, ErrDenied
	}
	var flags, mode, inBytes, outBytes, maxInstances, pid, handleFlags uint32
	if getHandleInformation.Find() != nil {
		return listenerState{}, ErrDenied
	}
	result, _, _ := getHandleInformation.Call(uintptr(l.handle), uintptr(unsafe.Pointer(&handleFlags)))
	if result == 0 {
		return listenerState{}, ErrDenied
	}
	if windows.GetNamedPipeInfo(l.handle, &flags, &outBytes, &inBytes, &maxInstances) != nil || windows.GetNamedPipeHandleState(l.handle, &mode, nil, nil, nil, nil, 0) != nil || windows.GetNamedPipeServerProcessId(l.handle, &pid) != nil {
		return listenerState{}, ErrDenied
	}
	security, err := nativeSecurity(l.handle, windows.SE_FILE_OBJECT)
	if err != nil {
		return listenerState{}, err
	}
	// identityは未解放のCreateNamedPipe返却handleとkernel server PID。pathを開き直さない。
	return listenerState{kind: kind, flags: flags, mode: mode, maxInstances: maxInstances, inBytes: inBytes, outBytes: outBytes, serverPID: pid, handleFlags: handleFlags, security: security}, nil
}
func (l *nativeListener) close() error {
	if l.handle != 0 {
		if windows.CloseHandle(l.handle) != nil {
			return ErrUnconfirmed
		}
		l.handle = 0
	}
	return nil
}
