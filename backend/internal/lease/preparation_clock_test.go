package lease

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNextN2PreparationClockBoundary(t *testing.T) {
	for _, phase := range []State{Prepared, Ready} {
		for _, which := range []string{"before-deadline", "at-deadline", "wall-only-forward"} {
			t.Run(string(phase)+"/"+which, func(t *testing.T) {
				base := time.Now()
				var sample atomic.Pointer[time.Time]
				sample.Store(&base)
				id := Identity{"prepare-clock", "epoch", strings.Repeat("a", 64)}
				a, err := newAuthority(id, 20*time.Second, 10*time.Second, 1, func() time.Time { return *sample.Load() })
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { a.Revoke(id); <-a.Done() })
				if phase == Ready && a.Transition(id, Prepared, Ready) != nil {
					t.Fatal("ready失敗")
				}
				now := base.Add(5 * time.Second)
				if which == "before-deadline" {
					now = now.Add(-time.Nanosecond)
				} else if which == "wall-only-forward" {
					// 単調情報を持たない偽clock。実OSの時計変更の再現ではない。
					now = now.Round(0)
				}
				sample.Store(&now)
				if which == "before-deadline" {
					if snap := a.Snapshot(); snap.State != phase || snap.Reason != "" {
						t.Fatalf("5秒直前に失効: %+v", snap)
					}
					select {
					case <-a.Context().Done():
						t.Fatal("5秒直前にcontext終了")
					default:
					}
					return
				}
				// Snapshotで失効させず、既存tickerが偽clock境界を検出する。
				select {
				case <-a.Context().Done():
				case <-time.After(100 * time.Millisecond):
					t.Fatal("要求なしで準備期限が失効しない")
				}
				if snap := a.Snapshot(); snap.State != Revoking || snap.Reason != "preparation-expired" {
					t.Fatalf("準備期限の失効理由が不正: %+v", snap)
				}
				recovered := base.Add(4 * time.Second)
				sample.Store(&recovered)
				if a.Transition(id, Prepared, Ready) == nil || a.Transition(id, Ready, Active) == nil || a.Renew(id, 0) == nil {
					t.Fatal("時計復旧後に準備期限失効から復活")
				}
			})
		}
	}
}
