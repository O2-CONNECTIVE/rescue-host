//go:build windows

package serviceready

import (
	"context"
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 発行thread専用scopeとread/write/take操作を持たない。
// 保持した値はInspectionLeaseだけが限定して借用する。
type inspectionNativeHandle struct {
	completion inspectionNativeCompletion
	handle     windows.Handle
	pending    bool
	work       *inspectionNativeWork
}
type inspectionNativeWork struct {
	ctx                              context.Context
	thread                           uint32
	pins                             runtime.Pinner
	raw                              bool
	anchored                         bool
	anchor                           [56]byte
	state                            inspectionState
	basic                            [14]uint32
	size, flags, pid                 uint32
	security                         [16384]byte
	user                             [256]byte
	groups                           [8192]byte
	before, after                    [56]byte
	kind, restricted, app, sizeToken uint32
	path                             [2048]uint16
	pathSize                         uint32
	creation, exit, kernel, userTime windows.Filetime
}

var inspectionGetSD = windows.NewLazySystemDLL("advapi32.dll").NewProc("GetKernelObjectSecurity")

func (h *inspectionNativeHandle) validate(access uint32) error {
	if h.completion.fatal {
		return ErrFatal
	}
	// 発行前にinspectInspectionで実GrantedAccessを照合する。
	if access != endpointInspectionAccess || h.pending || h.handle == 0 || h.handle == windows.InvalidHandle {
		return ErrDenied
	}
	return nil
}
func (h *inspectionNativeHandle) take() (uintptr, error) { return 0, ErrDenied }
func (h *inspectionNativeHandle) startWrite([]byte) (endpointWriteOperation, error) {
	return nil, ErrDenied
}
func (h *inspectionNativeHandle) borrowInspection() (uintptr, error) {
	if h.completion.fatal {
		return 0, ErrFatal
	}
	if h.pending || h.handle == 0 || h.handle == windows.InvalidHandle {
		return 0, ErrDenied
	}
	return uintptr(h.handle), nil
}
func (h *inspectionNativeHandle) close() error {
	if h.completion.fatal {
		return ErrFatal
	}
	if h.pending || h.work != nil {
		return ErrUnconfirmed
	}
	if h.handle != 0 {
		if windows.CloseHandle(h.handle) != nil {
			return ErrUnconfirmed
		}
		h.handle = 0
	}
	return nil
}
func (h *inspectionNativeHandle) begin(ctx context.Context) (*inspectionNativeWork, error) {
	if h.completion.fatal {
		return nil, ErrFatal
	}
	if h.pending || h.work != nil || !serviceWitnessLive(ctx) {
		return nil, ErrUnconfirmed
	}
	w := &inspectionNativeWork{ctx: ctx, thread: windows.GetCurrentThreadId()}
	h.work = w
	w.pins.Pin(w)
	return w, nil
}
func (h *inspectionNativeHandle) finish(w *inspectionNativeWork, b inspectionBinding, err error) error {
	return h.completion.complete(err, func() error {
		// 通常照会が取消で止まっても、実return後の最終主体観測を行う。
		w.raw = true
		_, terminalErr := w.subject(b)
		w.raw = false
		return terminalErr
	}, func() { w.pins.Unpin(); h.work = nil })
}
func (h *inspectionNativeHandle) trap() {
	if value := recover(); value != nil {
		h.completion.fatal = true
		panic(value) // work/pinと親の全参照はleaseが保持する。
	}
}
func (w *inspectionNativeWork) call(fn func() error) error {
	if w.thread == 0 || windows.GetCurrentThreadId() != w.thread {
		return ErrFatal
	}
	if !w.raw && !serviceWitnessLive(w.ctx) {
		return ErrDenied
	}
	err := fn()
	if windows.GetCurrentThreadId() != w.thread || errors.Is(err, ErrFatal) {
		return ErrFatal
	}
	if !w.raw && !serviceWitnessLive(w.ctx) {
		return ErrDenied
	}
	return err
}
func (h *inspectionNativeHandle) inspectionSubject(ctx context.Context, b inspectionBinding) (inspectionSubject, error) {
	defer h.trap()
	w, err := h.begin(ctx)
	if err != nil {
		return inspectionSubject{}, err
	}
	s, err := w.subject(b)
	return s, h.finish(w, b, err)
}
func (h *inspectionNativeHandle) inspectInspection(ctx context.Context, b inspectionBinding) (inspectionState, error) {
	defer h.trap()
	if err := h.validate(endpointInspectionAccess); err != nil {
		return inspectionState{}, err
	}
	w, err := h.begin(ctx)
	if err != nil {
		return inspectionState{}, err
	}
	s, err := w.inspect(h.handle, b)
	return s, h.finish(w, b, err)
}
func (w *inspectionNativeWork) inspect(h windows.Handle, b inspectionBinding) (inspectionState, error) {
	subject, err := w.subject(b)
	if err != nil {
		return inspectionState{}, err
	}
	if err = w.call(func() error { var e error; w.state.kind, e = windows.GetFileType(h); return e }); err != nil {
		return inspectionState{}, err
	}
	if err = w.call(func() error {
		ok, _, _ := getHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&w.flags)))
		if ok == 0 {
			return ErrDenied
		}
		return nil
	}); err != nil {
		return inspectionState{}, err
	}
	if err = w.call(func() error {
		status, _, _ := endpointQueryObject.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&w.basic[0])), unsafe.Sizeof(w.basic), uintptr(unsafe.Pointer(&w.size)))
		if status != 0 || w.size != 56 {
			return ErrDenied
		}
		return nil
	}); err != nil {
		return inspectionState{}, err
	}
	if err = w.call(func() error { return windows.GetNamedPipeServerProcessId(h, &w.pid) }); err != nil {
		return inspectionState{}, err
	}
	if err = w.call(func() error {
		ok, _, _ := inspectionGetSD.Call(uintptr(h), 5, uintptr(unsafe.Pointer(&w.security[0])), uintptr(len(w.security)), uintptr(unsafe.Pointer(&w.size)))
		if ok == 0 || w.size > uint32(len(w.security)) {
			return ErrDenied
		}
		return nil
	}); err != nil {
		return inspectionState{}, err
	}
	security, err := inspectionSecurity(w.security[:w.size])
	if err != nil {
		return inspectionState{}, err
	}
	after, err := w.subject(b)
	if err != nil || subject != after {
		return inspectionState{}, ErrFatal
	}
	w.state.access = w.basic[1]
	w.state.flags = w.flags
	w.state.serverPID = w.pid
	w.state.security = security
	w.state.subject = subject
	return w.state, nil
}
