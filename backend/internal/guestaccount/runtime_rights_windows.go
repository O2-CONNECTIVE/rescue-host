//go:build windows

package guestaccount

import (
	"context"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var runtimeLsaAdd = accountQueryAdvapi.NewProc("LsaAddAccountRights")
var runtimeLsaRemove = accountQueryAdvapi.NewProc("LsaRemoveAccountRights")

type runtimeRightsWindows struct {
	reader  permissionFactsWindows
	reads   []accountQueryMemory
	handle  windows.Handle
	attrs   accountQueryLSAAttributes
	sid     []byte
	values  [][]uint16
	strings []accountQueryLSAString
	opened  bool
}

func (k *runtimeRightsWindows) read(ctx context.Context, b binding, guard func() bool) ([]string, error) {
	m, err := k.reader.rights(b.sid, permissionFactsPhase{ctx: ctx, guard: guard})
	if m != nil {
		k.reads = append(k.reads, m)
	}
	if err != nil || m == nil || !finite(ctx) || !guard() {
		return nil, ErrDenied
	}
	o, err := m.decode()
	if err != nil || o.sid != b.sid || !finite(ctx) || !guard() {
		return nil, ErrDenied
	}
	return copyPermissionRights(o.rights)
}
func (k *runtimeRightsWindows) open(ctx context.Context, b binding, guard func() bool) error {
	if k.handle != 0 {
		if !k.opened {
			return ErrUnknown
		}
		return nil
	}
	var err error
	k.sid, err = permissionFactsSID(b.sid)
	if err != nil || !finite(ctx) || !guard() {
		return ErrDenied
	}
	k.attrs.length = uint32(unsafe.Sizeof(k.attrs))
	var pins runtime.Pinner
	pins.Pin(&k.attrs)
	pins.Pin(&k.handle)
	// Only this local SAM SID is modified. Group/GPO and existing subjects are
	// never inputs; CREATE_ACCOUNT permits a first direct rights assignment.
	status, _, _ := accountQueryLsaOpen.Call(0, uintptr(unsafe.Pointer(&k.attrs)), 0x810, uintptr(unsafe.Pointer(&k.handle)))
	pins.Unpin()
	if status == 0 && k.handle != 0 {
		k.opened = true
	} // retain actual open success before admission cancellation
	if !guard() {
		return ErrUnknown
	}
	if status != 0 || k.handle == 0 || !finite(ctx) {
		return ErrDenied
	}
	return nil
}
func (k *runtimeRightsWindows) write(ctx context.Context, b binding, rights []string, guard func() bool, remove bool) error {
	if len(rights) == 0 || len(rights) > 128 || !finite(ctx) || guard == nil || !guard() {
		return ErrDenied
	}
	if err := k.open(ctx, b, guard); err != nil {
		return err
	}
	k.values = make([][]uint16, len(rights))
	k.strings = make([]accountQueryLSAString, len(rights))
	var pins runtime.Pinner
	defer pins.Unpin()
	for i, right := range rights {
		wide, err := windows.UTF16FromString(right)
		if err != nil || len(wide) > 185 {
			return ErrDenied
		}
		k.values[i] = wide
		pins.Pin(&wide[0])
		k.strings[i] = accountQueryLSAString{length: uint16((len(wide) - 1) * 2), maximum: uint16(len(wide) * 2), buffer: &wide[0]}
	}
	pins.Pin(&k.sid[0])
	pins.Pin(&k.strings[0])
	if !finite(ctx) || !guard() {
		return ErrDenied
	}
	var status uintptr
	if remove {
		status, _, _ = runtimeLsaRemove.Call(uintptr(k.handle), uintptr(unsafe.Pointer(&k.sid[0])), 0, uintptr(unsafe.Pointer(&k.strings[0])), uintptr(len(k.strings)))
	} else {
		status, _, _ = runtimeLsaAdd.Call(uintptr(k.handle), uintptr(unsafe.Pointer(&k.sid[0])), uintptr(unsafe.Pointer(&k.strings[0])), uintptr(len(k.strings)))
	}
	if !guard() {
		return ErrUnknown
	}
	if status != 0 || !finite(ctx) {
		return ErrDenied
	}
	return nil
}
func (k *runtimeRightsWindows) add(ctx context.Context, b binding, v []string, g func() bool) error {
	return k.write(ctx, b, v, g, false)
}
func (k *runtimeRightsWindows) remove(ctx context.Context, b binding, v []string, g func() bool) error {
	return k.write(ctx, b, v, g, true)
}
func (k *runtimeRightsWindows) close(ctx context.Context, guard func() bool) error {
	for len(k.reads) > 0 {
		if !finite(ctx) || !guard() {
			return ErrCleanup
		}
		i := len(k.reads) - 1
		if err := k.reads[i].release(guard); err != nil {
			return err
		}
		k.reads = k.reads[:i]
	}
	if k.handle != 0 {
		if !finite(ctx) || !guard() {
			return ErrCleanup
		}
		status, _, _ := accountQueryLsaClose.Call(uintptr(k.handle))
		if status != 0 || !guard() {
			return ErrCleanup
		}
		k.handle = 0
	}
	return nil
}
