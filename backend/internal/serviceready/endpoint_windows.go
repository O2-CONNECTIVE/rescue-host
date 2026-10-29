//go:build windows

package serviceready

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native source only: no Factory/profile binding to Batch is supplied here.
type nativeEndpointPipe struct {
	nativeListener
	inspection *inspectionNativeHandle // retain out parameters before DuplicateHandle dispatch
}

func (nativeKernel) createEndpoint(name, descriptor string) (endpointDuplex, error) {
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		return nil, err
	}
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateNamedPipe(path,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 16384, 16384, 0, &sa)
	runtime.KeepAlive(sd)
	if h == 0 || h == windows.InvalidHandle {
		return nil, ErrDenied
	}
	return &nativeEndpointPipe{nativeListener: nativeListener{handle: h, thread: windows.GetCurrentThreadId()}}, err
}

func (p *nativeEndpointPipe) connect() (operation, error) {
	raw, err := p.nativeListener.connect()
	if raw == nil {
		return nil, err
	}
	return &endpointNativeConnect{raw: raw, thread: p.thread}, err
}

type endpointNativeConnect struct {
	raw    operation
	thread uint32
}

func (o *endpointNativeConnect) poll() (bool, error) {
	if windows.GetCurrentThreadId() != o.thread {
		return false, ErrFatal
	}
	return o.raw.poll()
}
func (o *endpointNativeConnect) cancel() error {
	if windows.GetCurrentThreadId() != o.thread {
		return ErrFatal
	}
	return o.raw.cancel()
}
func (o *endpointNativeConnect) release() error {
	if windows.GetCurrentThreadId() != o.thread {
		return ErrFatal
	}
	return o.raw.release()
}
func (p *nativeEndpointPipe) close() error {
	if windows.GetCurrentThreadId() != p.thread {
		return ErrFatal
	}
	return p.nativeListener.close()
}
func (p *nativeEndpointPipe) duplicate(access uint32) (endpointHandle, error) {
	if windows.GetCurrentThreadId() != p.thread {
		return nil, ErrFatal
	}
	if access == endpointInspectionAccess {
		h := &inspectionNativeHandle{pending: true}
		p.inspection = h
		err := windows.DuplicateHandle(windows.CurrentProcess(), p.handle, windows.CurrentProcess(), &h.handle, access, false, 0)
		h.pending = false
		if h.handle == 0 || h.handle == windows.InvalidHandle {
			return h, ErrDenied
		}
		return h, err
	}
	if access != endpointReadAccess && access != endpointWriteAccess {
		return nil, ErrDenied
	}
	var h windows.Handle
	err := windows.DuplicateHandle(windows.CurrentProcess(), p.handle, windows.CurrentProcess(), &h, access, false, 0)
	if h == 0 || h == windows.InvalidHandle {
		return nil, ErrDenied
	}
	return &endpointNativeHandle{handle: h, thread: p.thread}, err
}

type endpointNativeHandle struct {
	handle windows.Handle
	thread uint32
}

var endpointQueryObject = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")

func (h *endpointNativeHandle) validate(access uint32) error {
	if windows.GetCurrentThreadId() != h.thread {
		return ErrFatal
	}
	if h.handle == 0 || h.handle == windows.InvalidHandle || getHandleInformation.Find() != nil || endpointQueryObject.Find() != nil {
		return ErrDenied
	}
	kind, err := windows.GetFileType(h.handle)
	if err != nil || kind != windows.FILE_TYPE_PIPE {
		return ErrDenied
	}
	var flags uint32
	ok, _, _ := getHandleInformation.Call(uintptr(h.handle), uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || flags != 0 {
		return ErrDenied
	}
	// Documented PUBLIC_OBJECT_BASIC_INFORMATION is 56 bytes. Unknown results
	// reject; requested access is never treated as actual GrantedAccess.
	var basic [14]uint32
	var size uint32
	status, _, _ := endpointQueryObject.Call(uintptr(h.handle), 0, uintptr(unsafe.Pointer(&basic[0])), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&size)))
	if status != 0 || size != uint32(unsafe.Sizeof(basic)) || basic[1] != access {
		return ErrDenied
	}
	if access == endpointWriteAccess {
		// No read-attributes or mode/security query on this write-only handle.
		return nil
	}
	if access != endpointReadAccess {
		return ErrDenied
	}
	var pipeFlags, mode, pid uint32
	if windows.GetNamedPipeInfo(h.handle, &pipeFlags, nil, nil, nil) != nil || pipeFlags != 5 || windows.GetNamedPipeHandleState(h.handle, &mode, nil, nil, nil, nil, 0) != nil || mode != windows.PIPE_READMODE_MESSAGE || windows.GetNamedPipeServerProcessId(h.handle, &pid) != nil || pid != windows.GetCurrentProcessId() {
		return ErrDenied
	}
	return nil
}
func (h *endpointNativeHandle) take() (uintptr, error) {
	if windows.GetCurrentThreadId() != h.thread {
		return 0, ErrFatal
	}
	v := h.handle
	h.handle = 0
	return uintptr(v), nil
}
func (h *endpointNativeHandle) close() error {
	if windows.GetCurrentThreadId() != h.thread {
		return ErrFatal
	}
	if h.handle != 0 {
		if err := windows.CloseHandle(h.handle); err != nil {
			return err
		}
		h.handle = 0
	}
	return nil
}
