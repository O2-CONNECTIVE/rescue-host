//go:build windows

package operatorauth

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeProfileQueryKernel struct{ thread nativeThread }

// 正規endpoint leaseの発行は未接続。保持参照の候補照会を構成するだけで、
// raw handle・PIDの存在から正規operatorや同endpointを認証しない。
func newNativeProfileQuery(process, token uintptr) (*profileQuery, error) {
	return newProfileQuery(process, token, &nativeProfileQueryKernel{})
}
func (k *nativeProfileQueryKernel) subject() error {
	if k.thread.owner == 0 {
		k.thread.owner = windows.GetCurrentThreadId()
	}
	return k.thread.clean()
}
func (*nativeProfileQueryKernel) process(handle uintptr) (profileCandidateSnapshot, error) {
	if launchHandleAccess(handle, false, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE) != nil {
		return profileCandidateSnapshot{}, ErrDenied
	}
	h := windows.Handle(handle)
	state, e := windows.WaitForSingleObject(h, 0)
	if e != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return profileCandidateSnapshot{}, ErrDenied
	}
	pid, e := windows.GetProcessId(h)
	if e != nil || pid == 0 {
		return profileCandidateSnapshot{}, ErrDenied
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return profileCandidateSnapshot{}, ErrDenied
	}
	var path [32768]uint16
	size := uint32(len(path))
	if windows.QueryFullProcessImageName(h, 0, &path[0], &size) != nil || size == 0 || size >= uint32(len(path)) {
		return profileCandidateSnapshot{}, ErrDenied
	}
	return profileCandidateSnapshot{pid: pid, creation: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), imagePath: windows.UTF16ToString(path[:size])}, nil
}
func (*nativeProfileQueryKernel) token(handle uintptr) (profilePrimaryEvidence, error) {
	if launchHandleAccess(handle, false, windows.TOKEN_QUERY) != nil {
		return profilePrimaryEvidence{}, ErrDenied
	}
	t := windows.Token(handle)
	before, e := profileTokenStatistics(t)
	if e != nil {
		return profilePrimaryEvidence{}, e
	}
	p, e := launchPrimaryProjection(t)
	if e != nil {
		return profilePrimaryEvidence{}, e
	}
	after, e := profileTokenStatistics(t)
	if e != nil || before != after || before[0] != p.tokenID || before[1] != p.modifiedID {
		return profilePrimaryEvidence{}, ErrDenied
	}
	return profilePrimaryEvidence{projection: p.tokenProjection, tokenID: p.tokenID, modifiedID: p.modifiedID}, nil
}
func profileTokenStatistics(t windows.Token) ([2]uint64, error) {
	var storage [7]uint64
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 56)
	var size uint32
	if windows.GetTokenInformation(t, windows.TokenStatistics, &data[0], uint32(len(data)), &size) != nil || size != 56 || binary.LittleEndian.Uint32(data[24:28]) != windows.TokenPrimary {
		return [2]uint64{}, ErrDenied
	}
	return [2]uint64{binary.LittleEndian.Uint64(data[:8]), binary.LittleEndian.Uint64(data[48:56])}, nil
}
func (*nativeProfileQueryKernel) current(process uintptr) (uintptr, error) {
	var t windows.Token
	e := windows.OpenProcessToken(windows.Handle(process), windows.TOKEN_QUERY, &t)
	return uintptr(t), e
}
func (*nativeProfileQueryKernel) close(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}
