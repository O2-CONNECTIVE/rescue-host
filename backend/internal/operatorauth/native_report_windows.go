//go:build windows

package operatorauth

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type winReportKernel struct{}

var nativeReportDLL = windows.NewLazySystemDLL("kernel32.dll")
var nativeReportRead = nativeReportDLL.NewProc("ReadFile")
var nativeReportWrite = nativeReportDLL.NewProc("WriteFile")

func nativeReportKernel() (reportKernel, error) { return winReportKernel{}, nil }
func (winReportKernel) create(name, descriptor string) (uintptr, error) {
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		return 0, err
	}
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateNamedPipe(path,
		windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 0, 16384, 0, &sa)
	runtime.KeepAlive(sd)
	// Return partial acquisition to the owner even when the API reports failure.
	return uintptr(h), err
}
func (winReportKernel) open(name string) (uintptr, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	// Exact write rights exclude FILE_CREATE_PIPE_INSTANCE (0x4). No inherited
	// parent writer is created; only duplicate below may cross the child boundary.
	h, err := windows.CreateFile(path, 0x120102, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	return uintptr(h), err
}
func (winReportKernel) duplicate(h uintptr) (uintptr, error) {
	var child windows.Handle
	err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(h), windows.CurrentProcess(), &child, 0x120102, true, 0)
	return uintptr(child), err
}
func (winReportKernel) close(h uintptr) error { return windows.CloseHandle(windows.Handle(h)) }

var reportHandleInfo = nativeReportDLL.NewProc("GetHandleInformation")
var reportQueryObject = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")

func (winReportKernel) validate(handle uintptr, role endpointRole) error {
	h := windows.Handle(handle)
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_PIPE {
		return ErrDenied
	}
	if reportHandleInfo.Find() != nil || reportQueryObject.Find() != nil {
		return ErrDenied
	}
	var flags uint32
	ok, _, _ := reportHandleInfo.Call(handle, uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || flags != windows.HANDLE_FLAG_INHERIT {
		return ErrDenied
	}
	var basic [14]uint32
	var size uint32
	status, _, _ := reportQueryObject.Call(handle, 0, uintptr(unsafe.Pointer(&basic[0])), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&size)))
	if status != 0 || size != uint32(unsafe.Sizeof(basic)) {
		return ErrDenied
	}
	switch role {
	case endpointServerRead:
		var pipeFlags, mode uint32
		if basic[1] != 0x120089 || windows.GetNamedPipeInfo(h, &pipeFlags, nil, nil, nil) != nil || pipeFlags != 5 {
			return ErrDenied
		}
		if windows.GetNamedPipeHandleState(h, &mode, nil, nil, nil, nil, 0) != nil || mode != windows.PIPE_READMODE_MESSAGE {
			return ErrDenied
		}
	case endpointClientWrite:
		if basic[1] != 0x120102 {
			return ErrDenied
		}
		// GetNamedPipeInfo requires extra read-attributes access for a write-only
		// pipe. Do not broaden this handle. This check alone proves no provenance;
		// the private launch/report binding must establish the client endpoint.
	default:
		return ErrDenied
	}
	return nil
}

func (winReportKernel) clearInherit(h uintptr) error {
	return windows.SetHandleInformation(windows.Handle(h), windows.HANDLE_FLAG_INHERIT, 0)
}
