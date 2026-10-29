//go:build windows

package serviceready

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (w *inspectionNativeWork) clean() error {
	w.sizeToken = 0
	return w.call(func() error {
		err := windows.GetTokenInformation(windows.GetCurrentThreadToken(), windows.TokenType, (*byte)(unsafe.Pointer(&w.kind)), 4, &w.sizeToken)
		if err != windows.ERROR_NO_TOKEN {
			return ErrFatal
		}
		return nil
	})
}
func (w *inspectionNativeWork) tokenInfo(class uint32, out *byte, size uint32) error {
	w.sizeToken = 0
	return w.call(func() error {
		if windows.GetTokenInformation(windows.GetCurrentProcessToken(), class, out, size, &w.sizeToken) != nil || w.sizeToken > size {
			return ErrDenied
		}
		if class == windows.TokenStatistics && w.sizeToken == 56 && size >= 56 {
			// call内で観測値を固定し、直後の取消検査で成功観測を失わない。
			stats := *(*[56]byte)(unsafe.Pointer(out))
			if w.anchored && w.anchor != stats {
				return ErrFatal
			}
			w.anchor, w.anchored = stats, true
		}
		return nil
	})
}
func (w *inspectionNativeWork) subject(b inspectionBinding) (inspectionSubject, error) {
	if err := w.clean(); err != nil {
		return inspectionSubject{}, err
	}
	if err := w.tokenInfo(windows.TokenStatistics, &w.before[0], 56); err != nil {
		return inspectionSubject{}, err
	}
	if w.sizeToken != 56 {
		return inspectionSubject{}, ErrDenied
	}
	if err := w.tokenInfo(windows.TokenType, (*byte)(unsafe.Pointer(&w.kind)), 4); err != nil || w.sizeToken != 4 || w.kind != 1 {
		return inspectionSubject{}, ErrFatal
	}
	if err := w.tokenInfo(windows.TokenHasRestrictions, (*byte)(unsafe.Pointer(&w.restricted)), 4); err != nil {
		return inspectionSubject{}, err
	}
	if w.sizeToken != 4 {
		return inspectionSubject{}, ErrDenied
	}
	if err := w.tokenInfo(29, (*byte)(unsafe.Pointer(&w.app)), 4); err != nil {
		return inspectionSubject{}, err
	}
	if w.sizeToken != 4 {
		return inspectionSubject{}, ErrDenied
	}
	if err := w.tokenInfo(windows.TokenUser, &w.user[0], uint32(len(w.user))); err != nil {
		return inspectionSubject{}, err
	}
	user, err := inspectionTokenUser(w.user[:w.sizeToken])
	if err != nil {
		return inspectionSubject{}, err
	}
	if err = w.tokenInfo(windows.TokenGroups, &w.groups[0], uint32(len(w.groups))); err != nil {
		return inspectionSubject{}, err
	}
	attributes, err := inspectionServiceGroup(w.groups[:w.sizeToken], b.profile.Keys.ServiceSID)
	if err != nil {
		return inspectionSubject{}, err
	}
	w.pathSize = uint32(len(w.path))
	if err = w.call(func() error {
		return windows.QueryFullProcessImageName(windows.CurrentProcess(), 0, &w.path[0], &w.pathSize)
	}); err != nil {
		return inspectionSubject{}, err
	}
	if w.pathSize == 0 || w.pathSize >= uint32(len(w.path)) || windows.UTF16ToString(w.path[:w.pathSize]) != b.profile.SupervisorImage.Path {
		return inspectionSubject{}, ErrDenied
	}
	if err = w.call(func() error {
		return windows.GetProcessTimes(windows.CurrentProcess(), &w.creation, &w.exit, &w.kernel, &w.userTime)
	}); err != nil {
		return inspectionSubject{}, err
	}
	w.state.currentPID = windows.GetCurrentProcessId()
	w.state.creation = uint64(w.creation.HighDateTime)<<32 | uint64(w.creation.LowDateTime)
	if w.state.currentPID != b.pid || w.state.creation == 0 || w.state.creation != b.creation {
		return inspectionSubject{}, ErrDenied
	}
	if err = w.tokenInfo(windows.TokenStatistics, &w.after[0], 56); err != nil || w.sizeToken != 56 || w.before != w.after {
		return inspectionSubject{}, ErrFatal
	}
	if err = w.clean(); err != nil {
		return inspectionSubject{}, err
	}
	s := inspectionSubject{user: user, kind: w.kind, serviceAttributes: attributes, tokenID: binary.LittleEndian.Uint64(w.before[:8]), authenticationID: binary.LittleEndian.Uint64(w.before[8:16]), modifiedID: binary.LittleEndian.Uint64(w.before[48:56]), restricted: w.restricted != 0, appContainer: w.app != 0}
	// cleanは存在しないthread tokenを照会する。primary型は同じ統計値から取る。
	s.kind = binary.LittleEndian.Uint32(w.before[24:28])
	if !validInspectionSubject(s, b) {
		return inspectionSubject{}, ErrFatal
	}
	return s, nil
}
