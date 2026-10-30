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
	p, inspection, expectation, api, ctx := lifecycleGuestTokenFixture(t)
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
	// 呼出総数だけでなく、各openが返した別参照の成功回数を独立に判定する。
	if len(api.queries) != 3 || api.failedCloses != 1 {
		t.Fatal("3つの保持参照または初回の回収失敗を観測できません")
	}
	for _, query := range api.queries {
		if api.closedQueries[query] != 1 {
			t.Fatal("queryの回収漏れまたは同じ参照の二重閉鎖があります")
		}
	}
	closed := api.closeAttempts
	if err := p.Wait(ctx); !errors.Is(err, ErrExit) {
		t.Fatal("再観測で異常終了の分類を失いました")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal("確認済みの所有資源を再び未確認にしました")
	}
	if api.closeAttempts != closed || p.GuestTokenCurrent(inspection, observation) {
		t.Fatal("確認済みのqueryを再閉鎖するか古い観測を復活させました")
	}
}

// OS seamの返却参照と閉鎖だけを記録し、Prepared内部の参照fieldはoracleにしない。
type lifecycleGuestTokenMemory struct {
	*guestTokenMemory
	queries                     []uintptr
	closedQueries               map[uintptr]int
	closeAttempts, failedCloses int
}

func (m *lifecycleGuestTokenMemory) inspectTarget(spec Spec, process, thread, job uintptr) (uintptr, error) {
	query, err := m.inspectionMemory.inspectTarget(spec, process, thread, job)
	m.queries = append(m.queries, query)
	return query, err
}

func (m *lifecycleGuestTokenMemory) close(handle uintptr) error {
	if handle < 5 {
		return m.inspectionMemory.close(handle)
	}
	m.closeAttempts++
	err := m.inspectionMemory.close(handle)
	if err != nil {
		m.failedCloses++
	} else {
		m.closedQueries[handle]++
	}
	return err
}

func lifecycleGuestTokenFixture(t *testing.T) (*Prepared, *Inspection, GuestTokenExpectation, *lifecycleGuestTokenMemory, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	const user = "S-1-5-21-7-8-9-2048"
	expectation, err := NewGuestTokenExpectation(user, []string{"S-1-1-0", "S-1-5-2", "S-1-5-11", "S-1-5-32-545"}, []string{"SeChangeNotifyPrivilege"})
	if err != nil {
		t.Fatal("既存の合成期待値を準備できません")
	}
	facts := guestTokenFacts{user: user, owner: user, primaryGroup: "S-1-5-21-7-8-9-513", kind: 1, authentication: 3, tokenID: 8, modifiedID: 9, session: 1, integrity: 0x2000, elevationType: 1, groups: []guestTokenGroup{{"S-1-1-0", 7}, {"S-1-5-2", 7}, {"S-1-5-11", 7}, {"S-1-5-32-545", 7}}, privileges: []guestTokenPrivilege{{"SeChangeNotifyPrivilege", 3, 1}}}
	api := &lifecycleGuestTokenMemory{guestTokenMemory: &guestTokenMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}, facts: facts}, closedQueries: make(map[uintptr]int)}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal("同じ合成対象を準備できません")
	}
	t.Cleanup(func() {
		cleanup, end := context.WithTimeout(context.Background(), time.Second)
		defer end()
		if err := p.Close(cleanup); err != nil {
			t.Error("同じ合成対象の回収を確認できません")
		}
	})
	i, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal("同じ合成対象を検査できません")
	}
	return p, i, expectation, api, ctx
}
