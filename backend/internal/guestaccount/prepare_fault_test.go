package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPreparationFaultsNeverAdoptOrRepeatCreation(t *testing.T) {
	for _, tc := range []struct {
		name, op              string
		known, corrupt, panic bool
		sid                   bool
		want                  []string
	}{
		{"reserve-unknown", "reserve", false, false, false, false, []string{"reserve"}},
		{"attempt-commit-unknown", "commit:2", false, false, false, false, []string{"reserve", "commit:2"}},
		{"reserve-receipt", "reserve", false, true, false, false, []string{"reserve"}},
		{"attempt-panic", "commit:2", false, false, true, false, []string{"reserve", "commit:2"}},
		{"collision", "create", true, false, false, false, []string{"reserve", "commit:2", "create"}},
		{"create-unknown", "create", false, false, false, false, []string{"reserve", "commit:2", "create"}},
		{"create-panic", "create", false, false, true, false, []string{"reserve", "commit:2", "create"}},
		{"foreign-create-receipt", "create", false, true, false, false, []string{"reserve", "commit:2", "create"}},
		{"owned-commit-unknown", "commit:3", false, false, false, true, []string{"reserve", "commit:2", "create", "commit:3"}},
		{"prepared-commit-unknown", "commit:4", false, false, false, true, []string{"reserve", "commit:2", "create", "commit:3", "inspect", "commit:4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := newFixture(t)
			if tc.corrupt {
				f.corrupt[tc.op] = true
			} else if tc.panic {
				f.panics[tc.op] = true
			} else if tc.known {
				f.fail[tc.op] = knownFailure{}
			} else {
				f.fail[tc.op] = errors.New("synthetic uncertain result")
			}
			if err := l.Prepare(testContext(t)); err == nil {
				t.Fatal("faultで準備成功")
			}
			s, _ := l.Status()
			if s.State == Prepared || (s.SID != "") != tc.sid || !s.CleanupRequired {
				t.Fatalf("fault status=%+v", s)
			}
			if !reflect.DeepEqual(f.trace, tc.want) {
				t.Fatalf("許可外step=%v want=%v", f.trace, tc.want)
			}
			if err := l.Prepare(testContext(t)); !errors.Is(err, ErrDenied) || !reflect.DeepEqual(f.trace, tc.want) {
				t.Fatalf("createを再試行: %v %v", err, f.trace)
			}
		})
	}
}

func TestPreparationRejectsIncompleteOrExcessiveEffectiveAuthority(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*accountSnapshot)
	}{
		{"groups-unobserved", func(s *accountSnapshot) { s.groups = nil }},
		{"rights-unobserved", func(s *accountSnapshot) { s.logonRights = nil }},
		{"incomplete", func(s *accountSnapshot) { s.complete = false }},
		{"enabled", func(s *accountSnapshot) { s.disabled = false }},
		{"administrator", func(s *accountSnapshot) { s.administrator = true }},
		{"indirect-effective-group", func(s *accountSnapshot) { s.groups = append(s.groups, "synthetic-indirect-admin") }},
		{"privilege", func(s *accountSnapshot) { s.privileges = []string{"synthetic-privilege"} }},
		{"logon-right", func(s *accountSnapshot) { s.logonRights = []string{"synthetic-other-logon"} }},
		{"protected-write", func(s *accountSnapshot) { s.protectedWrites = true }},
		{"foreign-SID", func(s *accountSnapshot) { s.binding.sid = "S-1-5-21-10-20-30-9999" }},
		{"wrong-seal", func(s *accountSnapshot) { s.seal = &seal{9} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := newFixture(t)
			f.inspectHook = tc.alter
			if err := l.Prepare(testContext(t)); !errors.Is(err, ErrCleanup) {
				t.Fatalf("過剰/未取得authority受理=%v", err)
			}
			if f.stored.stage != ownedSID {
				t.Fatal("拒否後にpreparedを記録")
			}
		})
	}
}

func TestPublicGateAndFiniteContextPerformNoAccountOperations(t *testing.T) {
	if l, err := NewNative(); l != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("native gate=%v %v", l, err)
	}
	var zero Lifecycle
	if err := zero.Prepare(testContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	if err := zero.Close(testContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	l, f := newFixture(t)
	canceled, cancel := context.WithCancel(testContext(t))
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), canceled} {
		if err := l.Prepare(ctx); !errors.Is(err, ErrDenied) {
			t.Fatalf("無期限/中止ctx=%v", err)
		}
		if err := l.Close(ctx); !errors.Is(err, ErrDenied) {
			t.Fatalf("無期限/中止close=%v", err)
		}
	}
	if len(f.trace) != 0 {
		t.Fatalf("gate前に操作=%v", f.trace)
	}
	copyOwner := Lifecycle{self: l, plan: l.plan, sources: l.sources}
	if err := copyOwner.Prepare(testContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("複製owner=%v", err)
	}
}
