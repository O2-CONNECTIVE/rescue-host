//go:build windows

package guestaccount

import (
	"context"
	"encoding/binary"
	"errors"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"golang.org/x/sys/windows"
)

type stagingWindows struct {
	plan    Plan
	config  stagingNativeConfig
	ready   *serviceready.Factory
	query   accountQueryWindows
	scope   *stagingWindowsScope
	refs    []*stagingWindowsResource
	control *stagingControlRef
	active  bool
}
type stagingWindowsScope struct {
	lease   *bootstrapauth.PreparedLease
	witness *serviceready.ServiceWitness
}

func (s *stagingWindowsScope) Recheck(ctx context.Context) error {
	if s == nil || s.lease == nil || s.witness == nil {
		return ErrUnconfigured
	}
	if e := s.lease.Recheck(ctx); e != nil {
		return e
	}
	return s.witness.RecheckService(ctx)
}
func (s *stagingWindowsScope) Close(ctx context.Context) error {
	if s.witness != nil {
		if e := s.witness.CloseService(ctx); e != nil {
			return e
		}
		s.witness = nil
	}
	if s.lease != nil {
		if e := s.lease.Close(ctx); e != nil {
			return e
		}
		s.lease = nil
	}
	return nil
}

type stagingWindowsSlot struct {
	value   windows.Handle
	service bool
}
type stagingWindowsResource struct {
	slots []*stagingWindowsSlot
	guard func() bool
}

func (r *stagingWindowsResource) value() uintptr {
	if r == nil || len(r.slots) == 0 {
		return 0
	}
	return uintptr(r.slots[len(r.slots)-1].value)
}
func (r *stagingWindowsResource) close(ctx context.Context) error {
	for n := len(r.slots) - 1; n >= 0; n-- {
		slot := r.slots[n]
		if slot.value == 0 {
			continue
		}
		if !finite(ctx) || !realJournalHandle(uintptr(slot.value)) || r.guard == nil || !r.guard() {
			return ErrUnknown
		}
		var e error
		if slot.service {
			e = windows.CloseServiceHandle(slot.value)
		} else {
			e = windows.CloseHandle(slot.value)
		}
		if e != nil {
			return ErrCleanup
		}
		slot.value = 0
		if !r.guard() {
			return bootstrapauth.ErrFatal
		}
	}
	return nil
}

func stagingPlatform(p Plan, c stagingNativeConfig, ready *serviceready.Factory) (stagingOps, error) {
	k := &stagingWindows{plan: p, config: c, ready: ready}
	return stagingOps{thread: k.query.threadID, principal: k.query.principal, scope: k.prepare, open: k.open, inspect: k.inspect, launch: k.launch, journal: newNativeJournal, accounts: func(p Plan, s *seal, profile permissionProfile) (*creationSource, error) {
		return newNativeLimitedCreationSource(p, s, k.config.profile.native, profile)
	}}, nil
}
func (k *stagingWindows) prepare(ctx context.Context) (stagingScope, error) {
	s := &stagingWindowsScope{}
	k.scope = s
	var e error
	s.lease, e = k.config.prepared.HoldProfile(ctx, k.config.ready.Profile)
	// 以後は既存scope/factoryが複製したprofileだけを用いる。
	k.config.ready.Profile = bootstrapauth.Profile{}
	if e != nil {
		return s, e
	}
	s.witness, e = k.ready.PrepareServiceWitness(ctx)
	if e != nil || s.witness == nil {
		return s, errors.Join(ErrUnknown, e)
	}
	return s, s.witness.ConfirmServiceRunning(ctx)
}
func (k *stagingWindows) call(ctx context.Context, guard func() bool, call func() error) error {
	if !finite(ctx) || guard == nil || !guard() {
		return ErrDenied
	}
	k.active = true
	e := call()
	k.active = false
	if !guard() {
		return bootstrapauth.ErrFatal
	}
	if !finite(ctx) {
		return ErrDenied
	}
	return e
}

// 開く前に返却slotをkernelへ置く。部分returnやpanicも元adapterが保持する。
func (k *stagingWindows) open(ctx context.Context, kind permissionObjectKind, file stagingFile, guard func() bool) (stagingResource, error) {
	if kind == permissionObjectPipe {
		r := &stagingControlRef{lease: k.config.control}
		k.control = r
		// 借用call前からscopeとconfigで同じleaseを保持する。raw endpointは閉じない。
		e := k.call(ctx, guard, func() error { return r.obtain(ctx) })
		return r, e
	}
	r := &stagingWindowsResource{guard: guard}
	k.refs = append(k.refs, r)
	slot := func(service bool) *stagingWindowsSlot {
		s := &stagingWindowsSlot{service: service}
		r.slots = append(r.slots, s)
		return s
	}
	var e error
	switch kind {
	case permissionObjectService:
		manager := slot(true)
		e = k.call(ctx, guard, func() error {
			var e error
			manager.value, e = windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
			return e
		})
		if e != nil {
			return r, e
		}
		name, e := windows.UTF16PtrFromString(k.config.profile.serviceName)
		if e != nil {
			return r, e
		}
		service := slot(true)
		e = k.call(ctx, guard, func() error {
			var e error
			service.value, e = windows.OpenService(manager.value, name, windows.SERVICE_QUERY_STATUS|windows.READ_CONTROL)
			return e
		})
	case permissionObjectProcess:
		process := slot(false)
		var pid uint32
		e = k.call(ctx, guard, func() error { pid = windows.GetCurrentProcessId(); return nil })
		if e != nil {
			return r, e
		}
		e = k.call(ctx, guard, func() error {
			var e error
			process.value, e = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.READ_CONTROL, false, pid)
			return e
		})
	case permissionObjectToken:
		token := slot(false)
		e = k.call(ctx, guard, func() error {
			return windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.READ_CONTROL, (*windows.Token)(unsafe.Pointer(&token.value)))
		})
	case permissionObjectFile, permissionObjectDirectory:
		target := slot(false)
		name, e := windows.UTF16PtrFromString(file.path)
		if e != nil {
			return r, e
		}
		e = k.call(ctx, guard, func() error {
			var e error
			target.value, e = windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			h, e := stagingFileResult(uintptr(target.value), e)
			target.value = windows.Handle(h)
			return e
		})
	default:
		return r, ErrUnconfigured
	}
	return r, e
}
func (k *stagingWindows) inspect(ref stagingResource, guard func() bool) (journalFileInfo, error) {
	if guard == nil || !guard() {
		return journalFileInfo{}, bootstrapauth.ErrFatal
	}
	var filesystem [32]uint16
	if e := windows.GetVolumeInformationByHandle(windows.Handle(ref.value()), nil, 0, nil, nil, nil, &filesystem[0], uint32(len(filesystem))); e != nil {
		return journalFileInfo{}, e
	}
	if !guard() {
		return journalFileInfo{}, bootstrapauth.ErrFatal
	}
	if windows.UTF16ToString(filesystem[:]) != "NTFS" {
		return journalFileInfo{}, ErrDenied
	}
	// 既存journalの同handle metadata/DACL検査を再利用する。権限や設定は変更しない。
	raw := &nativeJournalKernel{root: journalRootCapability{service: k.plan.binding.service, serviceSID: k.config.profile.serviceSID}}
	info, e := raw.inspect(ref.value())
	if !guard() {
		return journalFileInfo{}, bootstrapauth.ErrFatal
	}
	return info, e
}
func (k *stagingWindows) launch(ctx context.Context, identity accountQueryPrincipal, guard func() bool) error {
	if k.control != nil {
		if e := k.control.recheck(ctx); e != nil {
			return e
		}
	} else if e := k.config.control.Recheck(ctx); e != nil {
		return e
	}
	if e := k.config.launch.Recheck(ctx); e != nil {
		return e
	}
	token, e := k.config.launch.Borrow(ctx)
	if e != nil || token != k.config.profile.native.token {
		return errors.Join(ErrDenied, e)
	}
	var flags uint32
	if e = k.call(ctx, guard, func() error {
		ok, _, _ := journalGetHandleInformation.Call(token, uintptr(unsafe.Pointer(&flags)))
		if ok == 0 {
			return ErrDenied
		}
		return nil
	}); e != nil || flags != 0 {
		return errors.Join(ErrDenied, e)
	}
	var stats [56]byte
	var needed uint32
	if e = k.call(ctx, guard, func() error {
		return windows.GetTokenInformation(windows.Token(token), windows.TokenStatistics, &stats[0], 56, &needed)
	}); e != nil {
		return e
	}
	if needed != 56 || binary.LittleEndian.Uint32(stats[24:28]) != 1 || binary.LittleEndian.Uint64(stats[:8]) != identity.tokenID || binary.LittleEndian.Uint64(stats[8:16]) != identity.authenticationID || binary.LittleEndian.Uint64(stats[48:56]) != identity.modifiedID {
		return ErrDenied
	}
	return nil
}
