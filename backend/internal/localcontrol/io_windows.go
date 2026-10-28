//go:build windows

package localcontrol

import (
	"context"
	"runtime"
	"time"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"golang.org/x/sys/windows"
)

type ioKind uint8

const (
	connectIO ioKind = iota
	readIO
	writeIO
)

// API関数を束ねるprivate試験seam。製品側はwindows API以外を注入できない。
type pipeCalls struct {
	event  func() (windows.Handle, error)
	start  func(ioKind, windows.Handle, []byte, *windows.Overlapped) error
	wait   func(windows.Handle, uint32) (uint32, error)
	result func(windows.Handle, *windows.Overlapped, *uint32, bool) error
	cancel func(windows.Handle, *windows.Overlapped) error
	close  func(windows.Handle) error
	now    func() time.Time
	fatal  func()
}

var pipeDLL = windows.NewLazySystemDLL("kernel32.dll")
var readPipe = pipeDLL.NewProc("ReadFile")
var writePipe = pipeDLL.NewProc("WriteFile")

func nativeCalls() pipeCalls {
	return pipeCalls{
		event: func() (windows.Handle, error) { return windows.CreateEvent(nil, 1, 0, nil) },
		start: func(kind ioKind, handle windows.Handle, buffer []byte, overlap *windows.Overlapped) error {
			switch kind {
			case connectIO:
				return windows.ConnectNamedPipe(handle, overlap)
			case readIO, writeIO:
				if len(buffer) == 0 {
					return ErrDenied
				}
				proc := readPipe
				if kind == writeIO {
					proc = writePipe
				}
				// asyncではcount=NULL。転送数を完了前に参照するwrapperを通さない。
				result, _, err := proc.Call(uintptr(handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, uintptr(unsafe.Pointer(overlap)))
				if result == 0 {
					return err
				}
				return nil
			default:
				return ErrDenied
			}
		},
		wait: windows.WaitForSingleObject, result: windows.GetOverlappedResult,
		cancel: windows.CancelIoEx, close: windows.CloseHandle, now: time.Now, fatal: nativeAPI{}.fatal,
	}
}

func (nativeAPI) connect(ctx context.Context, handle uintptr) error {
	_, err := pipeOperation(ctx, windows.Handle(handle), nil, connectIO, nativeCalls())
	return err
}

func (nativeAPI) read(ctx context.Context, handle uintptr) ([]byte, error) {
	return readMessage(ctx, windows.Handle(handle), nativeCalls())
}
func readMessage(ctx context.Context, handle windows.Handle, api pipeCalls) ([]byte, error) {
	buffer := make([]byte, control.FrameBytes)
	n, err := pipeOperation(ctx, handle, buffer, readIO, api)
	if err != nil || n == 0 || n > uint32(len(buffer)) {
		return nil, ErrDenied
	}
	return buffer[:n], nil
}

func (nativeAPI) write(ctx context.Context, handle uintptr, frame []byte) error {
	return writeMessage(ctx, windows.Handle(handle), frame, nativeCalls())
}
func writeMessage(ctx context.Context, handle windows.Handle, frame []byte, api pipeCalls) error {
	if len(frame) == 0 || len(frame) > control.FrameBytes {
		return ErrDenied
	}
	n, err := pipeOperation(ctx, handle, frame, writeIO, api)
	if err != nil || n != uint32(len(frame)) {
		return ErrDenied
	}
	return nil
}

// CancelIoEx成功は完了ではない。完了前にOVERLAPPED/buffer/eventを解放しない。
// OSが取消完了を返さない場合は固定fatalでprocessを終了し、成功を返さない。
func pipeOperation(ctx context.Context, handle windows.Handle, buffer []byte, kind ioKind, api pipeCalls) (uint32, error) {
	if ctx == nil || ctx.Err() != nil {
		return 0, ErrDenied
	}
	if _, ok := ctx.Deadline(); !ok {
		return 0, ErrDenied
	}
	event, err := api.event()
	if err != nil || event == 0 || event == windows.InvalidHandle {
		return 0, ErrDenied
	}
	unsafeExit := func() { api.fatal(); panic("制御IPCのI/O終了を確認できません") }
	defer func() {
		if api.close(event) != nil {
			unsafeExit()
		}
	}()
	overlap := windows.Overlapped{HEvent: event}
	var pins runtime.Pinner
	pins.Pin(&overlap)
	if len(buffer) > 0 {
		pins.Pin(&buffer[0])
	}
	defer pins.Unpin()
	err = api.start(kind, handle, buffer, &overlap)
	if kind == connectIO && err == windows.ERROR_PIPE_CONNECTED {
		return 0, nil
	}
	if err != nil && err != windows.ERROR_IO_PENDING {
		return 0, ErrDenied
	}
	var cleanupDeadline time.Time
	cancelled := false
	if err == windows.ERROR_IO_PENDING {
		for {
			if ctx.Err() != nil && !cancelled {
				cancelled = true
				cleanupDeadline = api.now().Add(time.Second)
				if cancelErr := api.cancel(handle, &overlap); cancelErr != nil && cancelErr != windows.ERROR_NOT_FOUND {
					unsafeExit()
				}
			}
			state, waitErr := api.wait(event, 10)
			if waitErr != nil || (state != windows.WAIT_OBJECT_0 && state != uint32(windows.WAIT_TIMEOUT)) {
				unsafeExit()
			}
			if state == windows.WAIT_OBJECT_0 {
				break
			}
			if cancelled && !api.now().Before(cleanupDeadline) {
				unsafeExit()
			}
		}
	}
	var transferred uint32
	err = api.result(handle, &overlap, &transferred, false)
	runtime.KeepAlive(buffer)
	if err == windows.ERROR_IO_INCOMPLETE {
		unsafeExit()
	}
	if err != nil || cancelled || ctx.Err() != nil {
		return 0, ErrDenied
	}
	return transferred, nil
}
