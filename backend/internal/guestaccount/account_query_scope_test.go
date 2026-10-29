package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAccountQueryLosingSubjectOrPanickingRetainsAllResourcesWithoutFree(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*accountQueryFixture)
	}{
		{"primary-drift", func(f *accountQueryFixture) { f.userHook = func() { f.principalValue.modifiedID++ } }},
		{"thread-drift", func(f *accountQueryFixture) { f.userHook = func() { f.thread++ } }},
		{"clean-thread-lost", func(f *accountQueryFixture) { f.userHook = func() { f.principalValue.threadToken = true } }},
		{"subject-query-error", func(f *accountQueryFixture) { f.userHook = func() { f.principalErr = ErrDenied } }},
		{"guard-panic", func(f *accountQueryFixture) { f.userHook = func() { f.principalPanic = true } }},
		{"decode-panic", func(f *accountQueryFixture) { f.user.decodePanic = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, f, b := accountQueryTest(t)
			tc.alter(f)
			if d, err := q.query(testContext(t), b); !errors.Is(err, ErrUnknown) || d.sid != "" {
				t.Errorf("scope喪失をsticky unknownにしない: %+v %v", d, err)
			}
			for _, step := range f.trace {
				if strings.HasPrefix(step, "free:") {
					t.Errorf("scope喪失後のnative cleanup再開: %v", f.trace)
					break
				}
			}
			if len(q.pending) != 3 {
				t.Errorf("取得済み資源参照を保持しない: %d", len(q.pending))
			}
			before := append([]string(nil), f.trace...)
			principalCalls := f.principalCalls
			// 観測値が健康に戻っても、同ownerのsticky fatalを解除しない。
			f.principalPanic = false
			f.principalErr = nil
			f.thread = 7
			f.principalValue.threadToken = false
			if _, err := q.query(testContext(t), b); !errors.Is(err, ErrUnknown) {
				t.Errorf("fatal後のquery=%v", err)
			}
			if !reflect.DeepEqual(f.trace, before) || f.principalCalls != principalCalls {
				t.Errorf("fatal後にnative query/cleanup再開: %v", f.trace)
			}
		})
	}
}

func TestAccountQueryStopsCleanupImmediatelyAfterFreeLosesScope(t *testing.T) {
	for _, panicGuard := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary-drift", true: "guard-panic"}[panicGuard], func(t *testing.T) {
			q, f, b := accountQueryTest(t)
			f.releaseHook = func(string) {
				if panicGuard {
					f.principalPanic = true
				} else {
					f.principalValue.modifiedID++
				}
			}
			_, err := q.query(testContext(t), b)
			var retire accountQueryScopeFatal
			if !errors.Is(err, ErrUnknown) || !errors.As(err, &retire) {
				t.Fatalf("退役marker未伝播=%v", err)
			}
			frees := 0
			for _, step := range f.trace {
				if strings.HasPrefix(step, "free:") {
					frees++
				}
			}
			if frees != 1 || len(q.pending) != 4 {
				t.Fatalf("最初のFree後のscope喪失で続行: frees=%d refs=%d trace=%v", frees, len(q.pending), f.trace)
			}
			before := append([]string(nil), f.trace...)
			if q.releasePending(func() bool { t.Error("fatalでguardを再開"); return true }) {
				t.Fatal("fatalのcleanupを成功扱い")
			}
			if !reflect.DeepEqual(before, f.trace) {
				t.Fatal("fatalでnative cleanupを再開")
			}
		})
	}
}

func TestAccountQueryFatalRetainsUnreturnedSourceResourceAndRejectsCopiedOwner(t *testing.T) {
	q, f, b := accountQueryTest(t)
	f.userHook = func() { panic("synthetic API return uncertainty") }
	_, err := q.query(testContext(t), b)
	var retire accountQueryScopeFatal
	if !errors.As(err, &retire) || !errors.Is(err, ErrUnknown) {
		t.Fatalf("source例外の退役marker=%v", err)
	}
	if len(q.pending) != 2 || f.inflight == nil {
		t.Fatalf("pending/inflight参照喪失: pending=%d inflight=%v", len(q.pending), f.inflight)
	}
	for _, step := range f.trace {
		if strings.HasPrefix(step, "free:") {
			t.Fatalf("source例外後にcleanup=%v", f.trace)
		}
	}
	// mutex自体を複製せず、浅いowner copyのself/kernel/resource headerを再現する。
	copyOwner := accountQuery{self: q, plan: q.plan, binding: q.binding, seal: q.seal, kernel: q.kernel, pending: q.pending, fatal: q.fatal}
	copyOwner.pending = nil // headerの変更で正本の保持参照を失わない。
	before := append([]string(nil), f.trace...)
	principalCalls := f.principalCalls
	if _, err := copyOwner.query(testContext(t), b); err != ErrUnconfigured {
		t.Fatalf("copyからquery=%v", err)
	}
	if copyOwner.releasePending(func() bool { t.Error("copyからscope probe"); return true }) {
		t.Fatal("copyからcleanup成功")
	}
	if len(q.pending) != 2 || f.inflight == nil || f.principalCalls != principalCalls || !reflect.DeepEqual(before, f.trace) {
		t.Fatal("copyが未知参照/native操作に影響")
	}
}

func TestAccountQueryLastFreeScopeLossIsFatalEvenWhenContextCanceled(t *testing.T) {
	q, f, b := accountQueryTest(t)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	frees := 0
	f.releaseHook = func(string) {
		frees++
		if frees == 5 {
			cancel()
			f.principalValue.modifiedID++
		}
	}
	_, err := q.query(ctx, b)
	var retire accountQueryScopeFatal
	if !errors.As(err, &retire) || !q.fatal {
		t.Fatalf("ctx取消が最後のscope喪失を隠した: fatal=%t error=%v", q.fatal, err)
	}
}

func TestAccountQueryResourceWithMultipleNativePartsGuardsEveryRelease(t *testing.T) {
	q, f, b := accountQueryTest(t)
	f.user.nativeParts = 2
	f.releaseHook = func(label string) {
		if label == "user" {
			f.principalValue.threadToken = true
		}
	}
	_, err := q.query(testContext(t), b)
	var retire accountQueryScopeFatal
	if !errors.As(err, &retire) {
		t.Fatalf("複合資源内scope喪失=%v", err)
	}
	for _, step := range f.trace {
		if step == "free:user:next" {
			t.Fatalf("複合資源の後段native cleanupを再開=%v", f.trace)
		}
	}
	if len(q.pending) != 3 {
		t.Fatalf("複合資源と未処理資源を保持しない=%d", len(q.pending))
	}
}
