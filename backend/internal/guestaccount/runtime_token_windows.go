//go:build windows

package guestaccount

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var runtimeLogonUser = accountQueryAdvapi.NewProc("LogonUserW")

type runtimeTokenWindows struct {
	serviceSID     string
	raw, primary   windows.Token
	authentication uint64
	scope          func() bool
	closed         bool
}

func (t *runtimeTokenWindows) acquire(ctx context.Context, b binding, secret []uint16, guard func() bool) error {
	if t.raw != 0 || t.primary != 0 || t.closed || t.scope == nil || guard == nil || !guard() || !finite(ctx) || len(secret) != 37 || secret[36] != 0 {
		return ErrDenied
	}
	name, err := windows.UTF16FromString(b.name)
	if err != nil {
		return ErrDenied
	}
	domain := []uint16{'.', 0}
	var pins runtime.Pinner
	pins.Pin(&name[0])
	pins.Pin(&domain[0])
	pins.Pin(&secret[0])
	pins.Pin(&t.raw)
	ok, _, _ := runtimeLogonUser.Call(uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&domain[0])), uintptr(unsafe.Pointer(&secret[0])), 4, 0, uintptr(unsafe.Pointer(&t.raw)))
	pins.Unpin()
	runtime.KeepAlive(secret)
	if !guard() {
		return ErrUnknown
	}
	if ok == 0 || t.raw == 0 || !finite(ctx) {
		return ErrDenied
	}
	t.authentication, err = runtimeTokenIdentity(t.raw, b.sid)
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:SYG:SYD:P(A;;0x000f01ff;;;SY)(A;;0x000f01ff;;;%s)", t.serviceSID))
	if err != nil || sd == nil || !finite(ctx) || !guard() {
		return ErrDenied
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	// The explicit SYSTEM owner may require SeRestorePrivilege. Missing rights
	// refuse acquisition; there is no adjustment, elevation or permissive retry.
	err = windows.DuplicateTokenEx(t.raw, windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.READ_CONTROL, &sa, windows.SecurityImpersonation, windows.TokenPrimary, &t.primary)
	runtime.KeepAlive(sd)
	if !guard() {
		return ErrUnknown
	}
	if err != nil || t.primary == 0 || !finite(ctx) {
		return ErrDenied
	}
	if err = t.inspect(ctx, b, guard); err != nil {
		return err
	}
	if !t.scope() || !finite(ctx) {
		return ErrCleanup
	}
	err = t.raw.Close()
	if err == nil {
		t.raw = 0
	} // retain actual release before inspecting admission again
	if err != nil || !t.scope() {
		return ErrCleanup
	}
	if !guard() {
		return ErrDenied
	}
	return nil
}
func runtimeTokenIdentity(token windows.Token, sid string) (uint64, error) {
	kind, err := accountQueryTokenDWORD(token, windows.TokenType)
	if err != nil || kind != windows.TokenPrimary {
		return 0, ErrDenied
	}
	u, err := token.GetTokenUser()
	if err != nil || u == nil || u.User.Sid == nil || !u.User.Sid.IsValid() || u.User.Sid.String() != sid {
		return 0, ErrDenied
	}
	var data [56]byte
	var size uint32
	if windows.GetTokenInformation(token, windows.TokenStatistics, &data[0], 56, &size) != nil || size != 56 || binary.LittleEndian.Uint32(data[24:28]) != windows.TokenPrimary {
		return 0, ErrDenied
	}
	auth := binary.LittleEndian.Uint64(data[8:16])
	if auth == 0 {
		return 0, ErrDenied
	}
	return auth, nil
}
func (t *runtimeTokenWindows) inspect(ctx context.Context, b binding, guard func() bool) error {
	if t.primary == 0 || !finite(ctx) || !guard() {
		return ErrDenied
	}
	auth, err := runtimeTokenIdentity(t.primary, b.sid)
	if err != nil || auth != t.authentication || !finite(ctx) || !guard() {
		return ErrDenied
	}
	return nil
}
func (t *runtimeTokenWindows) value() uintptr { return uintptr(t.primary) }
func (t *runtimeTokenWindows) Close(ctx context.Context) error {
	if t.closed {
		return nil
	}
	for _, slot := range []*windows.Token{&t.primary, &t.raw} {
		if *slot != 0 {
			if !finite(ctx) || t.scope == nil || !t.scope() {
				return ErrCleanup
			}
			if err := slot.Close(); err != nil || !t.scope() {
				return ErrCleanup
			}
			*slot = 0
		}
	}
	t.closed = true
	return nil
}
