package guestaccount

import (
	"context"
	"testing"
)

type stagingControlFixture struct {
	h                       uintptr
	err                     error
	checks, borrows, closes int
}

func (f *stagingControlFixture) Recheck(context.Context) error { f.checks++; return f.err }
func (f *stagingControlFixture) Borrow(context.Context) (uintptr, error) {
	f.borrows++
	return f.h, f.err
}
func (f *stagingControlFixture) Close(context.Context) error { f.closes++; return f.err }
func TestStagingControlRefKeepsSameEndpointAndRejectsRevokeOrDrift(t *testing.T) {
	for _, kind := range []string{"revoked", "different-handle"} {
		t.Run(kind, func(t *testing.T) {
			f := &stagingControlFixture{h: 111}
			r := &stagingControlRef{lease: f}
			if e := r.obtain(testContext(t)); e != nil {
				t.Fatal(e)
			}
			if e := r.recheck(testContext(t)); e != nil {
				t.Fatal(e)
			}
			if kind == "revoked" {
				f.err = ErrDenied
			} else {
				f.h = 112
			}
			if e := r.recheck(testContext(t)); e == nil {
				t.Fatal("退役/別endpointを受理")
			}
			f.h = 111
			f.err = nil
			if e := r.recheck(testContext(t)); e == nil {
				t.Fatal("退役したleaseを復活")
			}
			if e := r.close(testContext(t)); e != nil || f.closes != 1 {
				t.Fatal("logical lease返却なし", e)
			}
		})
	}
}
func TestStagingFileFailureSentinelIsNotAnOwnedHandle(t *testing.T) {
	if h, e := stagingFileResult(^uintptr(0), ErrDenied); h != 0 || e != ErrDenied {
		t.Fatal("既知未取得sentinelを保持", h, e)
	}
	if h, e := stagingFileResult(101, ErrDenied); h != 101 || e != ErrDenied {
		t.Fatal("部分real handleを失う", h, e)
	}
	if h, e := stagingFileResult(^uintptr(0), nil); h == 0 || e != nil {
		t.Fatal("未知の成功値を未取得へ変換", h, e)
	}
}
