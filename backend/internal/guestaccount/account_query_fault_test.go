package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAccountQueryRejectsUnconfirmedRoleMachineSubjectAndReusedName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*accountQueryFixture)
	}{
		{"DC-backup", func(f *accountQueryFixture) { f.role.value.role = 4 }},
		{"DC-primary", func(f *accountQueryFixture) { f.role.value.role = 5 }},
		{"unknown-role", func(f *accountQueryFixture) { f.role.value.role = 99 }},
		{"missing-role", func(f *accountQueryFixture) { f.role = nil }},
		{"role-error", func(f *accountQueryFixture) { f.roleErr = errors.New("role unknown") }},
		{"domain-error", func(f *accountQueryFixture) { f.domainErr = errors.New("domain unknown") }},
		{"foreign-machine", func(f *accountQueryFixture) { f.domain.value.sid = "S-1-5-21-40-50-60" }},
		{"primary-error", func(f *accountQueryFixture) { f.principalErr = errors.New("token query") }},
		{"other-primary-user", func(f *accountQueryFixture) { f.principalValue.user = "S-1-5-21-10-20-30-1001" }},
		{"impersonation-primary", func(f *accountQueryFixture) { f.principalValue.kind = 2 }},
		{"thread-token", func(f *accountQueryFixture) { f.principalValue.threadToken = true }},
		{"restricted", func(f *accountQueryFixture) { f.principalValue.restricted = true }},
		{"app-container", func(f *accountQueryFixture) { f.principalValue.appContainer = true }},
		{"reused-name", func(f *accountQueryFixture) { f.user.value.sid = "S-1-5-21-10-20-30-9999" }},
		{"renamed-account", func(f *accountQueryFixture) { f.user.value.name = "different-name" }},
		{"NERR_UserNotFound", func(f *accountQueryFixture) { f.userErr = errors.New("NERR_UserNotFound") }},
		{"ERROR_ACCESS_DENIED", func(f *accountQueryFixture) { f.userErr = errors.New("ERROR_ACCESS_DENIED") }},
		{"missing-user-buffer", func(f *accountQueryFixture) { f.user = nil }},
		{"decode-error", func(f *accountQueryFixture) { f.user.err = errors.New("invalid buffer") }},
		{"wrong-buffer-kind", func(f *accountQueryFixture) { f.user.value.kind = accountQueryDomain }},
		{"late-subject-change", func(f *accountQueryFixture) { f.userHook = func() { f.principalValue.modifiedID++ } }},
		{"late-thread-change", func(f *accountQueryFixture) { f.userHook = func() { f.thread++ } }},
		{"late-role-change", func(f *accountQueryFixture) { f.userHook = func() { f.role.value.role = 4 } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, f, b := accountQueryTest(t)
			tc.alter(f)
			d, err := q.query(testContext(t), b)
			if err == nil || d.sid != "" {
				t.Fatalf("未確認診断を受理=%+v %v", d, err)
			}
			s, err := q.inspect(testContext(t), b)
			if err == nil || s.complete || s.presence == absent {
				t.Fatalf("失敗を不在/完全権限へ昇格=%+v %v", s, err)
			}
		})
	}
}
func TestAccountQueryPartialBufferIsFreedBeforeReturningFailure(t *testing.T) {
	q, f, b := accountQueryTest(t)
	f.userErr = errors.New("partial native error")
	if _, err := q.query(testContext(t), b); err == nil {
		t.Fatal("部分error受理")
	}
	want := []string{"role", "decode:role", "domain", "decode:domain", "user:" + b.name, "free:user", "free:domain", "free:role"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("部分buffer所有/解放=%v", f.trace)
	}
}
func TestAccountQueryFreeFailureRetainsOwnerAndNeverBlindRetries(t *testing.T) {
	for _, panicFree := range []bool{false, true} {
		q, f, b := accountQueryTest(t)
		f.user.releaseErr = ErrCleanup
		f.user.releasePanic = panicFree
		if d, err := q.query(testContext(t), b); err == nil || d.sid != "" {
			t.Fatalf("free未確認診断=%+v %v", d, err)
		}
		before := append([]string(nil), f.trace...)
		wantPending := 1
		if panicFree {
			wantPending = 3
		} // panic後の未処理domain/roleも解放しない。
		if len(q.pending) != wantPending {
			t.Fatalf("failed bufferを失った=%d", len(q.pending))
		}
		if _, err := q.query(testContext(t), b); err == nil || !reflect.DeepEqual(f.trace, before) {
			t.Fatalf("未確認Freeを再実行=%v %v", f.trace, err)
		}
	}
}
func TestAccountQueryCancellationHoldsSourceUntilActualReturn(t *testing.T) {
	q, f, b := accountQueryTest(t)
	f.entered = make(chan struct{})
	f.unblock = make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(f.unblock)
		}
	}()
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := q.query(ctx, b); result <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("sourceへ入らなかった")
	}
	cancel()
	if _, err := q.query(testContext(t), b); !errors.Is(err, ErrBusy) {
		t.Fatalf("実return前にownerを放棄=%v", err)
	}
	for _, step := range f.trace {
		if step == "free:user" {
			t.Fatal("source実return前のFree")
		}
	}
	close(f.unblock)
	released = true
	if err := <-result; err == nil {
		t.Fatal("late canceled resultを受理")
	}
	found := false
	for _, step := range f.trace {
		if step == "free:user" {
			found = true
		}
	}
	if !found {
		t.Fatal("late bufferを所有していない")
	}
}
func TestAccountQueryRejectsMissingOrDifferentOwnedBindingWithoutCalls(t *testing.T) {
	for _, alter := range []func(*binding){func(b *binding) { b.sid = "" }, func(b *binding) { b.sid = b.machine + "-9999" }, func(b *binding) { b.epoch[0]++ }, func(b *binding) { b.intent[0]++ }, func(b *binding) { b.generation++ }, func(b *binding) { b.policy[0]++ }, func(b *binding) { b.machine = "S-1-5-21-40-50-60" }} {
		q, f, b := accountQueryTest(t)
		alter(&b)
		if _, err := q.query(testContext(t), b); err == nil || len(f.trace) != 0 {
			t.Fatalf("foreign owned binding=%v %v", err, f.trace)
		}
	}
	var zero accountQuery
	if _, err := zero.query(testContext(t), binding{}); err != ErrUnconfigured {
		t.Fatal(err)
	}
	q, f, b := accountQueryTest(t)
	if _, err := q.query(context.Background(), b); err == nil || len(f.trace) != 0 {
		t.Fatalf("無期限query=%v", err)
	}
}
