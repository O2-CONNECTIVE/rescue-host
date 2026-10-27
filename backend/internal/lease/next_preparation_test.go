package lease

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNextN2PreparationDeadline(t *testing.T) {
	for _, phase := range []State{Prepared, Ready} {
		t.Run(string(phase), func(t *testing.T) {
			id := Identity{"prepare-cap", "epoch", strings.Repeat("a", 64)}
			base := time.Now()
			var offset atomic.Int64
			a, err := newAuthority(id, 20*time.Second, 10*time.Second, 1, func() time.Time { return base.Add(time.Duration(offset.Load())) })
			if err != nil {
				t.Fatal(err)
			}
			defer func() { a.Revoke(id); <-a.Done() }()
			offset.Store(int64(4 * time.Second))
			if phase == Ready {
				if a.Transition(id, Prepared, Ready) != nil || a.Renew(id, 0) != nil {
					t.Fatal("ready更新失敗")
				}
			}
			offset.Store(int64(5 * time.Second))
			snap := a.Snapshot()
			if snap.State != Revoking || snap.Reason != "preparation-expired" {
				t.Fatal("固定5秒準備期限を延長")
			}
			if a.Transition(id, Prepared, Ready) == nil || a.Transition(id, Ready, Active) == nil || a.Renew(id, 1) == nil {
				t.Fatal("準備期限後に復活")
			}
		})
	}
}
func TestNextN2ActiveUsesLeaseAndMaximum(t *testing.T) {
	id := Identity{"active-cap", "epoch", strings.Repeat("b", 64)}
	base := time.Now()
	var offset atomic.Int64
	a, err := newAuthority(id, 20*time.Second, 10*time.Second, 1, func() time.Time { return base.Add(time.Duration(offset.Load())) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { a.Revoke(id); <-a.Done() }()
	offset.Store(int64(4 * time.Second))
	if a.Transition(id, Prepared, Ready) != nil || a.Transition(id, Ready, Active) != nil {
		t.Fatal("期限前のactivate失敗")
	}
	offset.Store(int64(6 * time.Second))
	if a.Snapshot().State != Active || a.Renew(id, 0) != nil {
		t.Fatal("activeを準備期限で終了")
	}
	offset.Store(int64(15 * time.Second))
	if a.Renew(id, 1) != nil {
		t.Fatal("更新失敗")
	}
	offset.Store(int64(20 * time.Second))
	if snap := a.Snapshot(); snap.State != Revoking || snap.Reason != "maximum-deadline" {
		t.Fatal("最大期限を延長")
	}
}
