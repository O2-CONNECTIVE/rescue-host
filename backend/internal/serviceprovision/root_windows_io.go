//go:build windows

package serviceprovision

import (
	"errors"
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

var rootDLL = windows.NewLazySystemDLL("kernel32.dll")
var rootGetHandleInfo = rootDLL.NewProc("GetHandleInformation")
var rootReadFile = rootDLL.NewProc("ReadFile")

func rootHandleFlags(handle windows.Handle) (uint32, error) {
	if rootGetHandleInfo.Find() != nil {
		return 0, ErrRootDenied
	}
	var flags uint32
	result, _, _ := rootGetHandleInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&flags)))
	if result == 0 {
		return 0, ErrRootDenied
	}
	return flags, nil
}

type rootNativeOperation struct {
	handle, event windows.Handle
	overlap       *windows.Overlapped
	buffer        []byte
	pins          runtime.Pinner
	terminal      bool
	n             uint32
	err           error
}

func (f *rootNativeFile) read(buffer []byte) (rootOperation, error) {
	if windows.GetCurrentThreadId() != f.thread {
		return nil, ErrRootFatal
	}
	if len(buffer) == 0 || rootReadFile.Find() != nil {
		return nil, ErrRootDenied
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	op := &rootNativeOperation{handle: f.handle, event: event, overlap: &windows.Overlapped{HEvent: event}, buffer: buffer}
	op.pins.Pin(op.overlap)
	op.pins.Pin(&buffer[0])
	// disk offset0、転送数NULL。Loaded.readFileが専用確保したbufferと
	// OVERLAPPEDを実completionまで保持/pinする。
	result, _, callErr := rootReadFile.Call(uintptr(f.handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, uintptr(unsafe.Pointer(op.overlap)))
	if result != 0 || errors.Is(callErr, windows.ERROR_IO_PENDING) {
		return op, nil
	}
	op.terminal = true
	op.err = callErr
	return op, callErr
}
func (o *rootNativeOperation) poll() (bool, uint32, error) {
	if o.terminal {
		return true, o.n, o.err
	}
	var n uint32
	err := windows.GetOverlappedResult(o.handle, o.overlap, &n, false)
	runtime.KeepAlive(o.buffer)
	if errors.Is(err, windows.ERROR_IO_INCOMPLETE) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, 0, nil
	}
	if err != nil && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) && !errors.Is(err, windows.ERROR_HANDLE_EOF) {
		return false, 0, err
	}
	o.terminal = true
	o.n = n
	o.err = err
	return true, n, err
}
func (o *rootNativeOperation) cancel() error {
	err := windows.CancelIoEx(o.handle, o.overlap)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}
func (o *rootNativeOperation) release() error {
	if !o.terminal {
		return ErrRootUnconfirmed
	}
	if o.event != 0 {
		if err := windows.CloseHandle(o.event); err != nil {
			return err
		}
		o.event = 0
	}
	o.pins.Unpin()
	o.buffer = nil
	o.overlap = nil
	return nil
}
