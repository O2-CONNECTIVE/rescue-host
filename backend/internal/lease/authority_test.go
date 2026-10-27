package lease

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testAuthority(t *testing.T) (*Authority, *atomic.Int64, Identity) {
	t.Helper()
	id := Identity{"launch", "epoch", strings.Repeat("a", 64)}
	var offset atomic.Int64
	base := time.Now()
	a, e := newAuthority(id, 2*time.Second, time.Second, 1, func() time.Time { return base.Add(time.Duration(offset.Load())) })
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Revoke(id); <-a.Done() })
	return a, &offset, id
}
func TestClockRegression(t *testing.T) {
	a, clock, id := testAuthority(t)
	a.Transition(id, Prepared, Ready)
	a.Transition(id, Ready, Active)
	clock.Store(-1)
	if a.Snapshot().Reason != "clock-regression" {
		t.Fatal("時計後退で失効しない")
	}
}
func TestClockMaximumAndLease(t *testing.T) {
	for _, cause := range []string{"lease", "maximum"} {
		t.Run(cause, func(t *testing.T) {
			a, clock, id := testAuthority(t)
			a.Transition(id, Prepared, Ready)
			a.Transition(id, Ready, Active)
			if cause == "maximum" {
				clock.Store(int64(900 * time.Millisecond))
				if e := a.Renew(id, 0); e != nil {
					t.Fatal(e)
				}
				clock.Store(int64(1800 * time.Millisecond))
				if e := a.Renew(id, 1); e != nil {
					t.Fatal(e)
				}
				clock.Store(int64(2 * time.Second))
			} else {
				clock.Store(int64(time.Second))
			}
			if a.Snapshot().State != Revoking {
				t.Fatal("期限到達で失効しない")
			}
			if cause == "maximum" && a.Snapshot().Reason != "maximum-deadline" {
				t.Fatal("最大期限が延びた")
			}
		})
	}
}
func TestReservationAccounting(t *testing.T) {
	a, _, id := testAuthority(t)
	a.Transition(id, Prepared, Ready)
	a.Transition(id, Ready, Active)
	r, e := a.Reserve()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Reserve(); e == nil {
		t.Fatal("接続中枠を二重利用")
	}
	a.Revoke(id)
	if r.Promote() == nil {
		t.Fatal("失効後に公開")
	}
	if a.Terminate() == nil {
		t.Fatal("接続中に終了扱い")
	}
	r.Release()
	r.Release()
	if e = a.Terminate(); e != nil {
		t.Fatal(e)
	}
}
func TestInvalidLeaseLimits(t *testing.T) {
	id := Identity{"launch", "epoch", strings.Repeat("a", 64)}
	for _, v := range []struct {
		max, lease time.Duration
		cap        int
	}{{0, time.Second, 1}, {3 * time.Hour, time.Second, 1}, {time.Second, 2 * time.Second, 1}, {time.Second, time.Second, 0}} {
		if a, e := New(id, v.max, v.lease, v.cap); e == nil {
			a.Revoke(id)
			<-a.Done()
			t.Fatal("範囲外設定を許可")
		}
	}
}
