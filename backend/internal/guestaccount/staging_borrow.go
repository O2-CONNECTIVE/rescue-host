package guestaccount

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

// adapterがlogical leaseだけを返す。Service所有endpointのraw handleを閉じない。
type stagingControlRef struct {
	lease           stagingControlLease
	handle          uintptr
	failed, closed  bool
	fatal, verified bool
}

func (r *stagingControlRef) value() uintptr { return r.handle }
func (r *stagingControlRef) obtain(ctx context.Context) error {
	if r.lease == nil || r.closed || r.failed || !finite(ctx) {
		return ErrDenied
	}
	if e := r.lease.Recheck(ctx); e != nil {
		r.failed = true
		return e
	}
	h, e := r.lease.Borrow(ctx)
	r.handle = h
	if e != nil || !realJournalHandle(h) || !finite(ctx) {
		r.failed = true
		if e != nil {
			return e
		}
		return ErrDenied
	}
	return nil
}
func (r *stagingControlRef) recheck(ctx context.Context) error {
	if r.failed || r.closed || !finite(ctx) || !realJournalHandle(r.handle) {
		return ErrDenied
	}
	if e := r.lease.Recheck(ctx); e != nil {
		r.failed = true
		return e
	}
	h, e := r.lease.Borrow(ctx)
	if e != nil || h != r.handle || !finite(ctx) {
		r.failed = true
		if e != nil {
			return e
		}
		return ErrDenied
	}
	return nil
}
func (r *stagingControlRef) close(ctx context.Context) error {
	if r.fatal {
		return bootstrapauth.ErrFatal
	}
	if r.closed {
		return nil
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	if e := r.lease.Close(ctx); e != nil {
		if errors.Is(e, bootstrapauth.ErrFatal) {
			r.fatal = true
		}
		return e
	}
	r.handle = 0
	r.closed = true
	return nil
}

// 要求pipeは短期照合だけで使う。SDK照会の実return前や不明scopeでは返却しない。
func (r *stagingControlRef) verifyAndRelease(ctx context.Context, b binding, serviceSID string, reader *permissionObjectReader, guard func() bool) (result error) {
	if r.fatal {
		return bootstrapauth.ErrFatal
	}
	defer func() {
		if recover() != nil {
			r.fatal = true
			result = bootstrapauth.ErrFatal
		}
	}()
	if r.verified && r.closed {
		return nil
	}
	if r.failed || r.closed || r.lease == nil || reader == nil || guard == nil || !finite(ctx) {
		return ErrDenied
	}
	defer func() {
		if reader.unsafe() || !guard() || errors.Is(result, bootstrapauth.ErrFatal) {
			r.fatal = true
			result = bootstrapauth.ErrFatal
		}
		if result != nil {
			r.failed = true
		}
	}()
	if e := r.obtain(ctx); e != nil {
		return e
	}
	object := permissionObject{role: roleControlPipe, kind: permissionObjectPipe, handle: r.handle}
	snapshot, e := reader.read(ctx, object, guard)
	if e != nil || !permissionObjectSafe(b, serviceSID, snapshot) {
		return ErrDenied
	}
	if e = r.recheck(ctx); e != nil {
		return e
	}
	if !guard() || !finite(ctx) {
		return ErrDenied
	}
	if e = r.close(ctx); e != nil {
		return e
	}
	if !finite(ctx) {
		return ErrDenied
	}
	r.verified = true
	return nil
}

// CreateFileWの既知未取得sentinelだけを0へする。error付きreal handleは保持する。
func stagingFileResult(h uintptr, e error) (uintptr, error) {
	if h == ^uintptr(0) && e != nil {
		return 0, e
	}
	return h, e
}
