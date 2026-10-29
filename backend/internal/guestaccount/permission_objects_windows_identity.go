//go:build windows

package guestaccount

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 全てborrowed handleへの読取だけ。open/duplicate/close/name検索をしない。
func (permissionObjectWindows) metadata(o permissionObject, w *permissionObjectWork, check func() bool) (permissionObjectMetadata, error) {
	var m permissionObjectMetadata
	h := windows.Handle(o.handle)
	call := func(f func() error) bool { return permissionObjectNativeCall(check, f) == nil }
	switch o.kind {
	case permissionObjectService:
		status := (*windows.SERVICE_STATUS_PROCESS)(unsafe.Pointer(&w.scratch[0]))
		if !call(func() error {
			return windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, &w.scratch[0], uint32(unsafe.Sizeof(*status)), &w.needed)
		}) {
			return m, ErrDenied
		}
		m.pid, m.state = status.ProcessId, status.CurrentState
		if !call(func() error { m.currentPID = windows.GetCurrentProcessId(); return nil }) {
			return m, ErrDenied
		}
	case permissionObjectProcess:
		if !call(func() error { var err error; m.pid, err = windows.GetProcessId(h); return err }) {
			return m, ErrDenied
		}
		if !call(func() error { m.currentPID = windows.GetCurrentProcessId(); return nil }) {
			return m, ErrDenied
		}
	case permissionObjectToken:
		if !call(func() error {
			return windows.GetTokenInformation(windows.Token(h), windows.TokenType, &w.scratch[0], 4, &w.needed)
		}) || w.needed != 4 {
			return m, ErrDenied
		}
		m.tokenType = binary.LittleEndian.Uint32(w.scratch[:4])
		if !call(func() error {
			return windows.GetTokenInformation(windows.Token(h), windows.TokenUser, &w.scratch[0], uint32(len(w.scratch)), &w.needed)
		}) || w.needed < uint32(unsafe.Sizeof(windows.Tokenuser{})) || w.needed > uint32(len(w.scratch)) {
			return m, ErrDenied
		}
		data := w.scratch[:w.needed]
		offset, ok := accountQueryOffset(accountQueryPointer(data, 0), uintptr(unsafe.Pointer(&data[0])), len(data), 8)
		if !ok {
			return m, ErrDenied
		}
		var err error
		m.tokenUser, err = accountQuerySID(data[offset:])
		if err != nil {
			return m, ErrDenied
		}
	case permissionObjectPipe:
		if !call(func() error { var err error; m.fileType, err = windows.GetFileType(h); return err }) {
			return m, ErrDenied
		}
	case permissionObjectFile, permissionObjectDirectory:
		if !call(func() error { var err error; m.fileType, err = windows.GetFileType(h); return err }) {
			return m, ErrDenied
		}
		basic := (*windows.ByHandleFileInformation)(unsafe.Pointer(&w.scratch[0]))
		if !call(func() error { return windows.GetFileInformationByHandle(h, basic) }) {
			return m, ErrDenied
		}
		m.links, m.directory, m.reparse = basic.NumberOfLinks, basic.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, basic.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		// FILE_ID_INFO: VolumeSerialNumber + FILE_ID_128。同じhandleを前後で読む。
		id := (*struct {
			Volume uint64
			ID     [16]byte
		})(unsafe.Pointer(&w.scratch[128]))
		if !call(func() error {
			return windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, &w.scratch[128], uint32(unsafe.Sizeof(*id)))
		}) {
			return m, ErrDenied
		}
		m.volume, m.fileID = id.Volume, id.ID
		standard := (*struct {
			Allocation, End          int64
			Links                    uint32
			DeletePending, Directory uint8
			_                        [2]byte
		})(unsafe.Pointer(&w.scratch[160]))
		if !call(func() error {
			return windows.GetFileInformationByHandleEx(h, windows.FileStandardInfo, &w.scratch[160], uint32(unsafe.Sizeof(*standard)))
		}) {
			return m, ErrDenied
		}
		if standard.Links != m.links || (standard.Directory != 0) != m.directory || standard.End < 0 {
			return m, ErrDenied
		}
		m.deletePending = standard.DeletePending != 0
	default:
		return m, ErrDenied
	}
	return m, nil
}
