package lease

import (
	"sync"
	"testing"
)

// 固定32ラウンド。失効より前の成功を違反とせず、確定後の許可を検査する。
func TestAFKUnit1ReservationRenewRevokeRace(t *testing.T) {
	for round := 0; round < 32; round++ {
		a, _, id := testAuthority(t)
		a.Transition(id, Prepared, Ready)
		a.Transition(id, Ready, Active)
		r, err := a.Reserve()
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, fn := range []func(){func() { r.Promote() }, func() { a.Renew(id, 1) }, func() { a.Revoke(id) }} {
			wg.Add(1)
			go func(f func()) { defer wg.Done(); <-start; f() }(fn)
		}
		close(start)
		wg.Wait()
		if a.Snapshot().State != Revoking || a.Renew(id, 2) == nil || r.Promote() == nil {
			t.Fatal("失効確定後の許可")
		}
		if _, err = a.Reserve(); err == nil {
			t.Fatal("確定後の予約")
		}
		r.Release()
		if a.Terminate() != nil {
			t.Fatal("競合後の回収失敗")
		}
	}
}
