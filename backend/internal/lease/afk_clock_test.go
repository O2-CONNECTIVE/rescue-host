package lease

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAFKUnit3ClockBoundaryFailClosed(t *testing.T) {
	for _, which := range []string{"panic-prepared", "panic-active", "zero-ready", "wall-only-backward", "wall-only-forward", "constructor-nil", "constructor-panic"} {
		t.Run(which, func(t *testing.T) {
			id := Identity{"clock-launch", "clock-epoch", strings.Repeat("a", 64)}
			if which == "constructor-nil" {
				if a, err := newAuthority(id, time.Second, time.Second, 1, nil); err == nil {
					a.Revoke(id)
					t.Fatal("nil clockを許可")
				}
				return
			}
			if which == "constructor-panic" {
				if a, err := newAuthority(id, time.Second, time.Second, 1, func() time.Time { panic("試験clock読取不能") }); err == nil {
					a.Revoke(id)
					t.Fatal("読取不能を許可")
				}
				return
			}
			base := time.Now()
			var fault atomic.Bool
			clock := func() time.Time {
				if !fault.Load() {
					return base
				}
				switch which {
				case "panic-prepared", "panic-active":
					panic("試験clock読取不能")
				case "zero-ready":
					return time.Time{}
				case "wall-only-backward":
					return time.Unix(0, base.UnixNano()-1)
				default:
					return time.Unix(0, base.UnixNano()+int64(10*time.Second))
				}
			}
			a, err := newAuthority(id, 2*time.Second, time.Second, 1, clock)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { a.Revoke(id); <-a.Done() })
			if which != "panic-prepared" {
				a.Transition(id, Prepared, Ready)
			}
			if which != "panic-prepared" && which != "zero-ready" {
				a.Transition(id, Ready, Active)
			}
			fault.Store(true)
			select {
			case <-a.Context().Done():
			case <-time.After(100 * time.Millisecond):
				t.Fatal("要求なしで時計故障が失効しない")
			}
			snap := a.Snapshot()
			if snap.State != Revoking {
				t.Fatal("時計故障で失効しない")
			}
			if which == "panic-prepared" || which == "panic-active" || which == "zero-ready" {
				if snap.Reason != "clock-unavailable" {
					t.Fatal("読取不能理由が不正")
				}
			}
			fault.Store(false)
			if a.Transition(id, Prepared, Ready) == nil || a.Transition(id, Ready, Active) == nil || a.Renew(id, 99) == nil {
				t.Fatal("時計復旧後に復活")
			}
			if _, err = a.Reserve(); err == nil {
				t.Fatal("時計復旧後に認可")
			}
		})
	}
}

func TestAFKUnit3PrepareMaximumIsNotReset(t *testing.T) {
	id := Identity{"prepared", "epoch", strings.Repeat("a", 64)}
	base := time.Now()
	var offset atomic.Int64
	a, err := newAuthority(id, 100*time.Millisecond, 80*time.Millisecond, 1, func() time.Time { return base.Add(time.Duration(offset.Load())) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { a.Revoke(id); <-a.Done() }()
	offset.Store(int64(70 * time.Millisecond))
	if a.Transition(id, Prepared, Ready) != nil || a.Renew(id, 0) != nil {
		t.Fatal("ready失敗")
	}
	offset.Store(int64(90 * time.Millisecond))
	if a.Transition(id, Ready, Active) != nil || a.Renew(id, 1) != nil {
		t.Fatal("active失敗")
	}
	offset.Store(int64(100 * time.Millisecond))
	if snap := a.Snapshot(); snap.State != Revoking || snap.Reason != "maximum-deadline" {
		t.Fatal("ready/activate/renewで最大期限が延びた")
	}
}
