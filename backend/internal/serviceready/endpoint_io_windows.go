//go:build windows

package serviceready

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var endpointWriteFile = readyDLL.NewProc("WriteFile")

type endpointNativeWrite struct {
	handle, event windows.Handle
	thread        uint32
	overlap       *windows.Overlapped
	buffer        []byte
	pins          runtime.Pinner
	terminal      bool
	n             uint32
	err           error
}

func (h *endpointNativeHandle) startWrite(buffer []byte) (endpointWriteOperation, error) {
	if windows.GetCurrentThreadId() != h.thread {
		return nil, ErrFatal
	}
	if len(buffer) == 0 || len(buffer) > 16384 || endpointWriteFile.Find() != nil {
		return nil, ErrDenied
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	o := &endpointNativeWrite{handle: h.handle, event: event, thread: h.thread, overlap: &windows.Overlapped{HEvent: event}, buffer: buffer}
	o.pins.Pin(o.overlap)
	o.pins.Pin(&buffer[0])
	// FILE_WRITE_DATA is sufficient for this single overlapped write. Poll a
	// separate event/OVERLAPPED with bWait=false; never wait on the file handle
	// or add SYNCHRONIZE/read-attribute/WRITE_ATTRIBUTES rights to the writer.
	// https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-writefile
	// https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-getoverlappedresult
	ok, _, callErr := endpointWriteFile.Call(uintptr(h.handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, uintptr(unsafe.Pointer(o.overlap)))
	if ok == 0 && !errors.Is(callErr, windows.ERROR_IO_PENDING) {
		// Synchronous initiation failure creates no pending request, but its
		// event and pinned memory are still owned and returned for cleanup.
		o.terminal, o.err = true, callErr
	}
	return o, nil
}
func (o *endpointNativeWrite) poll() (bool, uint32, error) {
	if windows.GetCurrentThreadId() != o.thread {
		return false, 0, ErrFatal
	}
	if o.terminal {
		return true, o.n, o.err
	}
	var n uint32
	err := windows.GetOverlappedResult(o.handle, o.overlap, &n, false)
	runtime.KeepAlive(o.buffer)
	if errors.Is(err, windows.ERROR_IO_INCOMPLETE) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, 0, nil
	}
	if err != nil && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) && !errors.Is(err, windows.ERROR_BROKEN_PIPE) && !errors.Is(err, windows.ERROR_NO_DATA) && !errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
		return false, 0, err
	}
	o.terminal, o.n, o.err = true, n, err
	return true, n, err
}
func (o *endpointNativeWrite) cancel() error {
	if windows.GetCurrentThreadId() != o.thread {
		return ErrFatal
	}
	err := windows.CancelIoEx(o.handle, o.overlap)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}
func (o *endpointNativeWrite) release() error {
	if windows.GetCurrentThreadId() != o.thread {
		return ErrFatal
	}
	if !o.terminal {
		return ErrUnconfirmed
	}
	if o.event != 0 {
		if err := windows.CloseHandle(o.event); err != nil {
			return err
		}
		o.event = 0
	}
	o.pins.Unpin()
	o.overlap, o.buffer = nil, nil
	return nil
}
