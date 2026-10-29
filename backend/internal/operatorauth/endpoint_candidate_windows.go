//go:build windows

package operatorauth

import (
	"golang.org/x/sys/windows"
)

type nativeEndpointCandidateKernel struct{ query nativeProfileQueryKernel }

func newNativeEndpointCandidateLease(input uintptr) (*endpointCandidateLease, error) {
	return newEndpointCandidateLease(input, &nativeEndpointCandidateKernel{})
}
func (k *nativeEndpointCandidateKernel) subject() error { return k.query.subject() }
func (*nativeEndpointCandidateKernel) endpoint(input uintptr) (uint32, error) {
	if e := validateNativeOperatorRead(input); e != nil {
		return 0, e
	}
	var pid uint32
	if windows.GetNamedPipeClientProcessId(windows.Handle(input), &pid) != nil || pid == 0 {
		return 0, ErrDenied
	}
	return pid, nil
}
func (*nativeEndpointCandidateKernel) openProcess(pid uint32) (uintptr, error) {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	return uintptr(h), e
}
func (*nativeEndpointCandidateKernel) openToken(process uintptr) (uintptr, error) {
	var t windows.Token
	e := windows.OpenProcessToken(windows.Handle(process), windows.TOKEN_QUERY, &t)
	return uintptr(t), e
}
func (k *nativeEndpointCandidateKernel) queryKernel() profileQueryKernel { return &k.query }
func (*nativeEndpointCandidateKernel) close(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}
