package guestaccount

import "context"

// adapterがlogical leaseだけを返す。Service所有endpointのraw handleを閉じない。
type stagingControlRef struct {
	lease          stagingControlLease
	handle         uintptr
	failed, closed bool
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
	if r.closed {
		return nil
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	if e := r.lease.Close(ctx); e != nil {
		return e
	}
	r.handle = 0
	r.closed = true
	return nil
}

// CreateFileWの既知未取得sentinelだけを0へする。error付きreal handleは保持する。
func stagingFileResult(h uintptr, e error) (uintptr, error) {
	if h == ^uintptr(0) && e != nil {
		return 0, e
	}
	return h, e
}
