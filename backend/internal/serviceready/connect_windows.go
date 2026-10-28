//go:build windows

package serviceready

import (
	"errors"
	"golang.org/x/sys/windows"
	"runtime"
)

type nativeOperation struct {
	handle, event windows.Handle
	overlap       *windows.Overlapped
	pins          runtime.Pinner
	terminal      bool
	err           error
}

func (l *nativeListener) connect() (operation, error) {
	if windows.GetCurrentThreadId() != l.thread {
		return nil, ErrFatal
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	op := &nativeOperation{handle: l.handle, event: event, overlap: &windows.Overlapped{HEvent: event}}
	op.pins.Pin(op.overlap)
	err = windows.ConnectNamedPipe(l.handle, op.overlap)
	if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		op.terminal = true
		return op, nil
	}
	if err == nil || errors.Is(err, windows.ERROR_IO_PENDING) {
		return op, nil
	}
	// 同期的な開始拒否はpending requestを作らない。所有物は返して回収する。
	op.terminal = true
	op.err = err
	return op, err
}
func (o *nativeOperation) poll() (bool, error) {
	if o.terminal {
		return true, o.err
	}
	var transferred uint32
	err := windows.GetOverlappedResult(o.handle, o.overlap, &transferred, false)
	if errors.Is(err, windows.ERROR_IO_INCOMPLETE) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	// query失敗をcompletionと仮定せず、既知のI/O終端だけを受け付ける。
	if err != nil && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) && !errors.Is(err, windows.ERROR_BROKEN_PIPE) && !errors.Is(err, windows.ERROR_NO_DATA) && !errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
		return false, err
	}
	o.terminal = true
	o.err = err
	return true, err
}
func (o *nativeOperation) cancel() error {
	err := windows.CancelIoEx(o.handle, o.overlap)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}
func (o *nativeOperation) release() error {
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
	o.overlap = nil
	return nil
}
