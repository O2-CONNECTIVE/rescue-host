//go:build windows

package operatorauth

import (
	"errors"
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

type nativeReportOperation struct {
	handle      windows.Handle
	overlap     *windows.Overlapped
	buffer      []byte
	pins        runtime.Pinner
	event       windows.Handle
	terminal    bool
	terminalErr error
	transferred uint32
}

func (winReportKernel) start(kind reportKind, h uintptr, b []byte) (reportOperation, error) {
	if kind != reportConnect && len(b) == 0 {
		return nil, errReportInvalid
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	op := &nativeReportOperation{handle: windows.Handle(h), event: event, overlap: &windows.Overlapped{HEvent: event}, buffer: b}
	op.pins.Pin(op.overlap)
	if len(b) > 0 {
		op.pins.Pin(&b[0])
	}
	switch kind {
	case reportConnect:
		err = windows.ConnectNamedPipe(op.handle, op.overlap)
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			op.terminal = true
			return op, nil
		}
	case reportRead, reportWrite:
		proc := nativeReportRead
		if kind == reportWrite {
			proc = nativeReportWrite
		}
		// 非同期APIの転送数はNULL。完了前にwrapperが転送数を参照しない。
		result, _, callErr := proc.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, uintptr(unsafe.Pointer(op.overlap)))
		if result == 0 {
			err = callErr
		}
	default:
		op.terminal = true
		op.terminalErr = errReportInvalid
		return op, nil
	}
	// 成功またはPENDINGなら実GetOverlappedResultが完了を返すまで保持する。
	// 同期的な開始拒否はpending requestを作らないのでterminalとして保持/回収する。
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		op.terminal = true
		op.terminalErr = translateReportIO(err)
	}
	return op, nil
}
func translateReportIO(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_MORE_DATA):
		return errReportMoreData
	case errors.Is(err, windows.ERROR_BROKEN_PIPE):
		return errReportBrokenPipe
	default:
		return err
	}
}
func (o *nativeReportOperation) poll() (bool, uint32, error) {
	if o.terminal {
		return true, o.transferred, o.terminalErr
	}
	var n uint32
	err := windows.GetOverlappedResult(o.handle, o.overlap, &n, false)
	runtime.KeepAlive(o.buffer)
	if errors.Is(err, windows.ERROR_IO_INCOMPLETE) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, 0, nil
	}
	// query自体の未知の失敗はrequestの終端証拠にしない。
	// 通常完了と、このpipe reportOperationの既知の終端statusだけを受け付ける。
	if err != nil && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) && !errors.Is(err, windows.ERROR_BROKEN_PIPE) && !errors.Is(err, windows.ERROR_MORE_DATA) && !errors.Is(err, windows.ERROR_NO_DATA) && !errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
		return false, 0, err
	}
	o.terminal = true
	o.transferred = n
	o.terminalErr = translateReportIO(err)
	return true, n, o.terminalErr
}
func (o *nativeReportOperation) cancel() error {
	err := windows.CancelIoEx(o.handle, o.overlap)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}
func (o *nativeReportOperation) release() error {
	if !o.terminal {
		return ErrUnconfirmed
	}
	// completion確認後だけevent/OVERLAPPED/bufferを解放する。close失敗時もownerを保持。
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
