//go:build windows

package serviceprovision

import (
	"context"
	"golang.org/x/sys/windows"
	"unsafe"
)

type launchNativeSnapshot struct{ handle windows.Handle }

func (launchNativeKernel) snapshot() (launchSnapshot, error) {
	handle, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil || handle == 0 || handle == windows.InvalidHandle {
		return nil, ErrLaunchDenied
	}
	return &launchNativeSnapshot{handle: handle}, nil
}
func (s *launchNativeSnapshot) threads(ctx context.Context, pid uint32) ([]uint32, error) {
	if !rootLive(ctx) || pid == 0 {
		return nil, ErrLaunchDenied
	}
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if windows.Thread32First(s.handle, &entry) != nil {
		return nil, ErrLaunchDenied
	}
	ids := make([]uint32, 0, 32)
	for scanned := 0; scanned < launchMaxSystemThreads; scanned++ {
		if !rootLive(ctx) || entry.Size < uint32(unsafe.Offsetof(entry.OwnerProcessID)+unsafe.Sizeof(entry.OwnerProcessID)) {
			return nil, ErrLaunchDenied
		}
		if entry.OwnerProcessID == pid {
			if len(ids) >= launchMaxThreads || entry.ThreadID == 0 {
				return nil, ErrLaunchDenied
			}
			ids = append(ids, entry.ThreadID)
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		err := windows.Thread32Next(s.handle, &entry)
		if err == windows.ERROR_NO_MORE_FILES {
			return ids, nil
		}
		if err != nil {
			return nil, ErrLaunchDenied
		}
	}
	return nil, ErrLaunchDenied
}
func (s *launchNativeSnapshot) close() error {
	if s.handle != 0 {
		if windows.CloseHandle(s.handle) != nil {
			return ErrLaunchUnconfirmed
		}
		s.handle = 0
	}
	return nil
}

type launchNativeThread struct {
	handle windows.Handle
	caller uint32
}

func (launchNativeKernel) openThread(tid uint32) (launchThreadRef, error) {
	handle, err := windows.OpenThread(0x120800, false, tid)
	if err != nil || handle == 0 {
		return nil, ErrLaunchDenied
	}
	return &launchNativeThread{handle: handle, caller: windows.GetCurrentThreadId()}, nil
}

var launchThreadDLL = windows.NewLazySystemDLL("kernel32.dll")
var launchGetThreadID = launchThreadDLL.NewProc("GetThreadId")
var launchGetThreadPID = launchThreadDLL.NewProc("GetProcessIdOfThread")
var launchGetThreadTimes = launchThreadDLL.NewProc("GetThreadTimes")

func (t *launchNativeThread) inspect() (launchThreadState, error) {
	if windows.GetCurrentThreadId() != t.caller {
		return launchThreadState{}, ErrLaunchFatal
	}
	if launchGetThreadID.Find() != nil || launchGetThreadPID.Find() != nil || launchGetThreadTimes.Find() != nil {
		return launchThreadState{}, ErrLaunchDenied
	}
	alive, err := windows.WaitForSingleObject(t.handle, 0)
	if err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return launchThreadState{}, ErrLaunchDenied
	}
	tid, _, _ := launchGetThreadID.Call(uintptr(t.handle))
	pid, _, _ := launchGetThreadPID.Call(uintptr(t.handle))
	if tid == 0 || pid == 0 {
		return launchThreadState{}, ErrLaunchDenied
	}
	var creation, exit, kernel, user windows.Filetime
	result, _, _ := launchGetThreadTimes.Call(uintptr(t.handle), uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if result == 0 {
		return launchThreadState{}, ErrLaunchDenied
	}
	security, err := launchNativeSecurity(t.handle)
	if err != nil {
		return launchThreadState{}, err
	}
	access, flags, err := launchNativePermissions(t.handle)
	if err != nil {
		return launchThreadState{}, err
	}
	alive, err = windows.WaitForSingleObject(t.handle, 0)
	if err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return launchThreadState{}, ErrLaunchDenied
	}
	return launchThreadState{pid: uint32(pid), tid: uint32(tid), creation: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), alive: true, flags: flags, access: access, security: security}, nil
}
func (t *launchNativeThread) close() error {
	if t.handle != 0 {
		if windows.CloseHandle(t.handle) != nil {
			return ErrLaunchUnconfirmed
		}
		t.handle = 0
	}
	return nil
}
