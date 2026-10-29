package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRestartUsesOldBoundCleanupAndNeverRestoresPreparation(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	f.oldBinding = f.stored.binding
	f.s.previous = f
	f.trace = nil
	recovered, err := recoverLifecycle(testContext(t), l.plan, f.s)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Prepare(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatalf("復旧からprepare=%v", err)
	}
	if err := recovered.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	want := []string{"load", "revoke-old", "disable", "quiesce-old", "inspect", "commit:5", "inspect", "delete", "inspect", "commit:6"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("旧対象の回収=%v want=%v", f.trace, want)
	}
}

func TestRestartCannotSubstituteChangedPolicy(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	f.oldBinding, f.s.previous = f.stored.binding, f
	p := l.plan
	in := ProtectedInputs{sessionEpoch: p.binding.epoch, intentID: p.binding.intent, generation: p.binding.generation, machineSID: p.binding.machine, operatorSID: p.binding.operator, serviceUserSID: p.binding.service, policy: limitedPolicy{known: true, groups: []string{"synthetic-admin"}, privileges: []string{}, logonRights: []string{}}}
	changed, err := NewPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoverLifecycle(testContext(t), changed, f.s); !errors.Is(err, ErrManual) {
		t.Fatalf("旧記録へ別policyを代入=%v", err)
	}
}

func TestRestartNeverAdoptsNoSIDCreateAttempt(t *testing.T) {
	l, f := newFixture(t)
	f.stored = record{binding: l.plan.binding, stage: createAttempt}
	f.oldBinding, f.s.previous = f.stored.binding, f
	f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: l.plan.binding, presence: exact, complete: true, disabled: true}
	f.snapshot.binding.sid = "S-1-5-21-10-20-30-1002"
	r, err := recoverLifecycle(testContext(t), l.plan, f.s)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(testContext(t)); !errors.Is(err, ErrManual) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.trace, []string{"load", "revoke-old"}) {
		t.Fatalf("prefix/nameから採用/削除=%v", f.trace)
	}
}
func TestRestartRequiresBoundPreviousSessionNotNewEmptyOwner(t *testing.T) {
	l, f := preparedFixture(t)
	if _, err := recoverLifecycle(testContext(t), l.plan, f.s); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("新しいsessionのみで旧回収=%v", err)
	}
	f.s.previous = f
	f.oldBinding = f.stored.binding
	f.oldBinding.epoch[0]++
	if _, err := recoverLifecycle(testContext(t), l.plan, f.s); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("別epoch回収=%v", err)
	}
}
func TestRestartOfDeleteAttemptNeverBlindRetries(t *testing.T) {
	l, f := preparedFixture(t)
	f.stored.stage = deleteAttempt
	f.oldBinding, f.s.previous = f.stored.binding, f
	r, err := recoverLifecycle(testContext(t), l.plan, f.s)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(testContext(t)); err == nil {
		t.Fatal("不明削除を成功扱い")
	}
	if !reflect.DeepEqual(f.trace, []string{"load", "revoke-old", "quiesce-old", "inspect"}) {
		t.Fatalf("再起動でdelete再実行=%v", f.trace)
	}
}
func TestRestartRejectsDifferentMachineEpochIntentAndSID(t *testing.T) {
	for _, change := range []func(*binding){
		func(b *binding) { b.epoch[0]++ }, func(b *binding) { b.intent[0]++ }, func(b *binding) { b.generation++ }, func(b *binding) { b.machine = "S-1-5-21-40-50-60" }, func(b *binding) { b.sid = "S-1-5-21-40-50-60-1002" }, func(b *binding) { b.sid = b.operator },
	} {
		l, f := preparedFixture(t)
		change(&f.stored.binding)
		f.oldBinding, f.s.previous = f.stored.binding, f
		if _, err := recoverLifecycle(testContext(t), l.plan, f.s); !errors.Is(err, ErrManual) {
			t.Fatalf("foreign binding復旧=%v", err)
		}
	}
}

func TestRecoveryNeedsOnlyPreviousSessionProducer(t *testing.T) {
	l, f := preparedFixture(t)
	f.oldBinding, f.s.previous = f.stored.binding, f
	f.s.session = nil
	r, err := recoverLifecycle(testContext(t), l.plan, f.s)
	if err != nil {
		t.Fatalf("旧producerがあるのに新sessionを必須化=%v", err)
	}
	if err := r.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRestartDeleteIntentCannotCompleteWithoutOldWorkloadProof(t *testing.T) {
	l, f := preparedFixture(t)
	f.stored.stage = deleteAttempt
	f.oldBinding, f.s.previous = f.stored.binding, f
	f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: f.stored.binding, presence: absent, complete: true}
	f.fail["quiesce-old"] = ErrUnknown
	r, err := recoverLifecycle(testContext(t), l.plan, f.s)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(testContext(t)); err == nil {
		t.Fatal("account不在だけで旧workload回収を完了した")
	}
	status, _ := r.Status()
	if status.State == Closed || !status.CleanupRequired {
		t.Fatalf("旧回収証拠がない状態=%+v", status)
	}
}

func TestReservedAbortCommitsTerminalBeforeClosing(t *testing.T) {
	l, f := newFixture(t)
	f.fail["commit:2"] = knownFailure{}
	if err := l.Prepare(testContext(t)); err == nil {
		t.Fatal("作成前中止を成功扱い")
	}
	f.trace = nil
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if f.stored.stage == reserved {
		t.Fatal("Closedでも予約記録が未清掃のまま")
	}
	f.s.previous = nil
	recovered, err := recoverLifecycle(testContext(t), l.plan, f.s)
	if err != nil {
		t.Fatalf("終端記録の復旧=%v", err)
	}
	status, _ := recovered.Status()
	if status.State != Closed || status.CleanupRequired {
		t.Fatalf("中止完了記録=%+v", status)
	}
}
func TestReservedAbortCommitFailureKeepsCleanupRequired(t *testing.T) {
	l, f := newFixture(t)
	f.fail["commit:2"] = knownFailure{}
	_ = l.Prepare(testContext(t))
	f.fail["commit:7"] = ErrUnknown
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("中止完了記録未確認なのに成功")
	}
	status, _ := l.Status()
	if status.State == Closed || !status.CleanupRequired {
		t.Fatalf("中止未確認=%+v", status)
	}
}

func TestConfirmedAbortCommitSurvivesCallerCancellation(t *testing.T) {
	l, f := newFixture(t)
	f.fail["commit:2"] = knownFailure{}
	_ = l.Prepare(testContext(t))
	ctx, cancel := context.WithCancel(testContext(t))
	f.journalHook = func(s stage) {
		if s == abortComplete {
			cancel()
		}
	}
	if err := l.Close(ctx); !errors.Is(err, ErrCleanup) {
		t.Fatalf("取消の返却=%v", err)
	}
	f.journalHook, f.trace = nil, nil
	if err := l.Close(testContext(t)); err != nil {
		t.Fatalf("保存確認済み中止の再終了=%v", err)
	}
	for _, op := range f.trace {
		if op == "commit:7" {
			t.Fatalf("保存済み中止を再commit=%v", f.trace)
		}
	}
}
