//go:build windows

package operatorauth

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (winReportKernel) primary() (string, map[string]uint32, error) {
	// Do not create under an impersonation token even if the primary identity
	// would match. Pseudo handles are query-only and have no close obligation.
	var kind, size uint32
	err := windows.GetTokenInformation(windows.GetCurrentThreadToken(), windows.TokenType, (*byte)(unsafe.Pointer(&kind)), 4, &size)
	if err != windows.ERROR_NO_TOKEN {
		return "", nil, ErrDenied
	}
	token := windows.GetCurrentProcessToken()
	if windows.GetTokenInformation(token, windows.TokenType, (*byte)(unsafe.Pointer(&kind)), 4, &size) != nil || size != 4 || kind != 1 {
		return "", nil, ErrDenied
	}
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return "", nil, ErrDenied
	}
	groups, err := token.GetTokenGroups()
	if err != nil || groups == nil || groups.GroupCount > 1024 {
		return "", nil, ErrDenied
	}
	enabled := make(map[string]uint32)
	for _, g := range groups.AllGroups() {
		if g.Sid == nil || !g.Sid.IsValid() {
			return "", nil, ErrDenied
		}
		sid := g.Sid.String()
		if _, exists := enabled[sid]; exists {
			return "", nil, ErrDenied
		}
		enabled[sid] = g.Attributes
	}
	name := user.User.Sid.String()
	runtime.KeepAlive(user)
	runtime.KeepAlive(groups)
	return name, enabled, nil
}
