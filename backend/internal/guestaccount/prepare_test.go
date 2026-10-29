package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPrepareDisabledRequiresDurableOwnershipAndLimitedSnapshot(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	s, err := l.Status()
	if err != nil || s.State != Prepared || s.SID != "S-1-5-21-10-20-30-1002" || !s.CleanupRequired {
		t.Fatalf("status=%+v error=%v", s, err)
	}
	want := []string{"reserve", "commit:2", "create", "commit:3", "inspect", "commit:4"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("安全な準備順序=%v want=%v", f.trace, want)
	}
	if err := l.Prepare(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatalf("作成再試行=%v", err)
	}
}

func TestPartialCreateReceiptSurvivesCancellationWithStickyUnknown(t *testing.T) {
	l, f := newFixture(t)
	ctx, cancel := context.WithCancel(testContext(t))
	f.partial = true
	f.fail["create"] = context.Canceled
	f.createHook = cancel
	if err := l.Prepare(ctx); !errors.Is(err, ErrUnknown) {
		t.Fatalf("部分作成cancel=%v", err)
	}
	s, _ := l.Status()
	if s.SID != "S-1-5-21-10-20-30-1002" || !s.Uncertain || !s.CleanupRequired {
		t.Fatalf("部分receiptが失われた: %+v", s)
	}
	delete(f.fail, "create")
	f.trace = nil
	if err := l.Close(testContext(t)); !errors.Is(err, ErrUnknown) {
		t.Fatalf("不明作成後close=%v", err)
	}
	want := []string{"revoke", "disable", "quiesce"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("unknownでは削除しない: %v", f.trace)
	}
}

func TestPrepareRejectsUnobservedPrivilegeList(t *testing.T) {
	l, f := newFixture(t)
	f.inspectHook = func(s *accountSnapshot) { s.privileges = nil }
	if err := l.Prepare(testContext(t)); !errors.Is(err, ErrCleanup) {
		t.Fatalf("未取得privilegeが安全な空集合になった: %v", err)
	}
}
