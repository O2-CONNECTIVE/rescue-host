//go:build windows

package bootstrapio

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type winKernel struct{}

var bootstrapDLL = windows.NewLazySystemDLL("kernel32.dll")
var bootstrapRead = bootstrapDLL.NewProc("ReadFile")
var bootstrapWrite = bootstrapDLL.NewProc("WriteFile")

func nativeKernel() (kernel, error) { return winKernel{}, nil }
func (winKernel) create(name, descriptor string) (uintptr, error) {
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		return 0, err
	}
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd, InheritHandle: 0}
	h, err := windows.CreateNamedPipe(path,
		windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 0, MaxRecordBytes, 0, &sa)
	runtime.KeepAlive(sd)
	if err != nil {
		return 0, err
	}
	return uintptr(h), nil
}
func (winKernel) open(name string) (uintptr, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	// nil SECURITY_ATTRIBUTES は非継承。writerの複製を一切生成しない。
	h, err := windows.CreateFile(path, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return 0, err
	}
	return uintptr(h), nil
}
func (winKernel) duplicate(h uintptr) (uintptr, error) {
	var child windows.Handle
	err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(h), windows.CurrentProcess(), &child, 0, true, windows.DUPLICATE_SAME_ACCESS)
	if err != nil {
		return 0, err
	}
	return uintptr(child), nil
}
func (winKernel) close(h uintptr) error { return windows.CloseHandle(windows.Handle(h)) }

type winOperation struct {
	handle      windows.Handle
	overlap     *windows.Overlapped
	buffer      []byte
	pins        runtime.Pinner
	event       windows.Handle
	terminal    bool
	terminalErr error
	transferred uint32
}

func (winKernel) start(kind ioKind, h uintptr, b []byte) (operation, error) {
	if kind != connectIO && len(b) == 0 {
		return nil, ErrInvalid
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	op := &winOperation{handle: windows.Handle(h), event: event, overlap: &windows.Overlapped{HEvent: event}, buffer: b}
	op.pins.Pin(op.overlap)
	if len(b) > 0 {
		op.pins.Pin(&b[0])
	}
	switch kind {
	case connectIO:
		err = windows.ConnectNamedPipe(op.handle, op.overlap)
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			op.terminal = true
			return op, nil
		}
	case readIO, writeIO:
		proc := bootstrapRead
		if kind == writeIO {
			proc = bootstrapWrite
		}
		// 非同期APIの転送数はNULL。完了前にwrapperが転送数を参照しない。
		result, _, callErr := proc.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, uintptr(unsafe.Pointer(op.overlap)))
		if result == 0 {
			err = callErr
		}
	default:
		op.terminal = true
		op.terminalErr = ErrInvalid
		return op, nil
	}
	// 成功またはPENDINGなら実GetOverlappedResultが完了を返すまで保持する。
	// 同期的な開始拒否はpending requestを作らないのでterminalとして保持/回収する。
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		op.terminal = true
		op.terminalErr = translateIO(err)
	}
	return op, nil
}
func translateIO(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_MORE_DATA):
		return errMoreData
	case errors.Is(err, windows.ERROR_BROKEN_PIPE):
		return errBrokenPipe
	default:
		return err
	}
}
func (o *winOperation) poll() (bool, uint32, error) {
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
	// 通常完了と、このpipe operationの既知の終端statusだけを受け付ける。
	if err != nil && !errors.Is(err, windows.ERROR_OPERATION_ABORTED) && !errors.Is(err, windows.ERROR_BROKEN_PIPE) && !errors.Is(err, windows.ERROR_MORE_DATA) && !errors.Is(err, windows.ERROR_NO_DATA) && !errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
		return false, 0, err
	}
	o.terminal = true
	o.transferred = n
	o.terminalErr = translateIO(err)
	return true, n, o.terminalErr
}
func (o *winOperation) cancel() error {
	err := windows.CancelIoEx(o.handle, o.overlap)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}
func (o *winOperation) release() error {
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
