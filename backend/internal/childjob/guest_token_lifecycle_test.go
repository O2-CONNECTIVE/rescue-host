package childjob

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// 既存の公開メソッドを組み合わせた後付け回帰。実token生成や実processは使わない。
func TestObservedGuestConcurrentCommitsHaveOnlyOneAcceptedStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lease, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		p, inspection, expectation, api, _ := guestTokenFixture(t, lease)
		observation, err := p.ObserveGuestToken(lease, inspection, expectation)
		if err != nil {
			t.Fatal("同じ対象の観測を準備できません")
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var first sync.Once
		api.onCall = func(name string) {
			if name == "inspect-target" {
				first.Do(func() { close(entered); <-release })
			}
		}
		result := make(chan error, 2)
		go func() { result <- p.CommitInspected(lease, inspection) }()
		<-entered
		go func() { result <- p.CommitInspected(lease, inspection) }()
		// 両方が同じ未消費観測で入り、後続が所有permitを待つ順序を固定する。
		synctest.Wait()
		close(release)
		accepted := 0
		for range 2 {
			if err := <-result; err == nil {
				accepted++
			} else if !errors.Is(err, ErrStart) {
				t.Fatal("二重開始の拒否分類が変化しました")
			}
		}
		resumes := 0
		for _, call := range api.calls {
			if call == "resume" {
				resumes++
			}
		}
		if accepted != 1 || resumes != 1 {
			t.Fatalf("単回開始を維持できません: accepted=%d resume=%d", accepted, resumes)
		}
		if p.GuestTokenCurrent(inspection, observation) || p.CommitInspected(lease, inspection) == nil {
			t.Fatal("消費した観測を再利用できました")
		}
	})
}

func TestObservedGuestAbnormalExitKeepsUnconfirmedQueryCleanupAndExitFailure(t *testing.T) {
	p, inspection, expectation, api, ctx := guestTokenFixture(t)
	observation, err := p.ObserveGuestToken(ctx, inspection, expectation)
	if err != nil {
		t.Fatal("同じ対象の観測を準備できません")
	}
	if err := p.CommitInspected(ctx, inspection); err != nil {
		t.Fatal("観測後の開始を確認できません")
	}
	api.exitCode, api.failure = 2, "close-query"
	if err := p.Wait(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("異常終了後のquery回収失敗を成功または確認済み退出に変えました")
	}
	if p.GuestTokenCurrent(inspection, observation) || p.CommitInspected(ctx, inspection) == nil {
		t.Fatal("回収未確認の対象を再実行できました")
	}
	api.failure = ""
	// 同じ所有者の再観測で、残ったqueryを回収しても元の異常終了は残す。
	if err := p.Wait(ctx); !errors.Is(err, ErrExit) {
		t.Fatal("回収の成功で異常終了を通常成功に変えました")
	}
	closed := 0
	for _, call := range api.calls {
		if call == "close-query" {
			closed++
		}
	}
	// 初回の失敗1回と、Inspect/Observe/Commitが保持した3参照の成功を数える。
	if closed != 4 {
		t.Fatalf("同じquery参照の回収が不足または重複しました: calls=%d", closed)
	}
	if err := p.Wait(ctx); !errors.Is(err, ErrExit) {
		t.Fatal("再観測で異常終了の分類を失いました")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal("確認済みの所有資源を再び未確認にしました")
	}
	remainingCalls := 0
	for _, call := range api.calls {
		if call == "close-query" {
			remainingCalls++
		}
	}
	if remainingCalls != closed || p.GuestTokenCurrent(inspection, observation) {
		t.Fatal("確認済みのqueryを再閉鎖するか古い観測を復活させました")
	}
}
