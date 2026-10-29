package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func preparedFixture(t *testing.T) (*Lifecycle, *fixture) {
	t.Helper()
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	f.trace = nil
	return l, f
}
func TestFailedWorkloadCleanupStillDisablesOwnedAccount(t *testing.T) {
	l, f := preparedFixture(t)
	f.snapshot.disabled = false
	f.fail["quiesce"] = context.DeadlineExceeded
	if err := l.Close(testContext(t)); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	if !f.snapshot.disabled || !reflect.DeepEqual(f.trace, []string{"revoke", "disable", "quiesce"}) {
		t.Fatalf("containment=%v %v", f.snapshot.disabled, f.trace)
	}
}
func TestCloseRejectsForeignOrIncompleteReceiptsBeforeDeletion(t *testing.T) {
	for _, op := range []string{"revoke", "disable", "quiesce", "inspect", "commit:5"} {
		t.Run(op, func(t *testing.T) {
			l, f := preparedFixture(t)
			f.corrupt[op] = true
			if err := l.Close(testContext(t)); err == nil {
				t.Fatal("foreign receipt受理")
			}
			for _, step := range f.trace {
				if step == "delete" {
					t.Fatalf("foreign receipt後削除=%v", f.trace)
				}
			}
		})
	}
}
func TestKnownDeleteFailureKeepsDisabledAndRechecksExactIdentity(t *testing.T) {
	l, f := preparedFixture(t)
	f.fail["delete"] = knownFailure{}
	if err := l.Close(testContext(t)); !errors.Is(err, ErrCleanup) {
		t.Fatal(err)
	}
	if !f.snapshot.disabled || f.snapshot.presence != exact {
		t.Fatal("失敗後のdisabled accountを保持しない")
	}
	delete(f.fail, "delete")
	f.trace = nil
	f.snapshot.binding.sid = "S-1-5-21-10-20-30-9999"
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("同名別SIDを採用")
	}
	for _, step := range f.trace {
		if step == "delete" {
			t.Fatalf("同名別SIDを削除=%v", f.trace)
		}
	}
}
func TestUnknownDeleteOnlyReconcilesAbsenceNeverRepeatsMutation(t *testing.T) {
	l, f := preparedFixture(t)
	f.fail["delete"] = errors.New("synthetic uncertain delete")
	if err := l.Close(testContext(t)); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	delete(f.fail, "delete")
	f.trace = nil
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("未確定削除を成功扱い")
	}
	if !reflect.DeepEqual(f.trace, []string{"revoke", "quiesce", "inspect"}) {
		t.Fatalf("不明削除を再実行=%v", f.trace)
	}
	f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: l.record.binding, presence: absent, complete: true}
	f.trace = nil
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.trace, []string{"revoke", "quiesce", "inspect", "commit:6"}) {
		t.Fatalf("不在照合=%v", f.trace)
	}
	s, _ := l.Status()
	if !s.Uncertain || s.State != Closed {
		t.Fatalf("unknown履歴消失=%+v", s)
	}
}
func TestDeleteSuccessJournalFailureNeverTouchesReusedName(t *testing.T) {
	l, f := preparedFixture(t)
	f.fail["commit:6"] = errors.New("synthetic commit outcome")
	if err := l.Close(testContext(t)); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	delete(f.fail, "commit:6")
	f.trace = nil
	f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: l.record.binding, presence: foreign, complete: true}
	f.snapshot.binding.sid = "S-1-5-21-10-20-30-9999"
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.trace, []string{"commit:6"}) {
		t.Fatalf("削除完了後の同名再作成を操作=%v", f.trace)
	}
}
func TestDeletePanicBecomesStickyAndReconcilesOnly(t *testing.T) {
	l, f := preparedFixture(t)
	f.panics["delete"] = true
	if err := l.Close(testContext(t)); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	delete(f.panics, "delete")
	f.trace = nil
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("panic後削除成功扱い")
	}
	if !reflect.DeepEqual(f.trace, []string{"revoke", "quiesce", "inspect"}) {
		t.Fatalf("panic後blind再実行=%v", f.trace)
	}
}

func TestMixedKnownAndUnknownDeleteNeverAuthorizesRetry(t *testing.T) {
	l, f := preparedFixture(t)
	f.fail["delete"] = errors.Join(knownFailure{}, ErrUnknown)
	if err := l.Close(testContext(t)); !errors.Is(err, ErrUnknown) {
		t.Fatalf("混合errorを既知扱い=%v", err)
	}
	delete(f.fail, "delete")
	f.trace = nil
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("混合error後成功扱い")
	}
	if !reflect.DeepEqual(f.trace, []string{"revoke", "quiesce", "inspect"}) {
		t.Fatalf("混合error後blinddelete=%v", f.trace)
	}
}

func TestDeletionRechecksExactDisabledAccountAfterIntentCommit(t *testing.T) {
	for _, change := range []string{"identity", "enabled"} {
		t.Run(change, func(t *testing.T) {
			l, f := preparedFixture(t)
			f.inspectHook = func(s *accountSnapshot) {
				if f.stored.stage == deleteAttempt {
					if change == "identity" {
						s.binding.sid = s.binding.machine + "-9999"
					} else {
						s.disabled = false
					}
				}
			}
			if err := l.Close(testContext(t)); err == nil {
				t.Fatal("保存後の対象変更を受理")
			}
			for _, step := range f.trace {
				if step == "delete" {
					t.Fatalf("保存前snapshotで削除=%v", f.trace)
				}
			}
		})
	}
}
