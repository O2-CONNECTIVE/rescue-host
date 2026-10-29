package guestaccount

import (
	"context"
	"errors"
	"testing"
)

type permissionGroupsFixture struct {
	memory accountQueryMemory
	err    error
	calls  int
	hook   func()
}

func (k *permissionGroupsFixture) snapshot(context.Context, binding, func() bool) (accountQueryMemory, error) {
	k.calls++
	if k.hook != nil {
		k.hook()
	}
	return k.memory, k.err
}

func TestPermissionAliasesCollectorRetainsAndCopiesExactGraph(t *testing.T) {
	q, f, b := accountQueryTest(t)
	graph := []permissionGroup{{sid: "S-1-5-32-545", members: []string{b.sid}}}
	m := &accountQueryFixtureMemory{f: f, label: "groups", value: accountQueryObservation{kind: accountQueryGroups, groups: graph}}
	k := &permissionGroupsFixture{memory: m}
	c, e := newPermissionGroupsCollector(q, k)
	if e != nil {
		t.Fatal(e)
	}
	got, e := c.collect(testContext(t), b, func() bool { return true })
	if e != nil || len(got) != 1 || got[0].members[0] != b.sid || len(q.pending) != 1 {
		t.Fatal(got, e, len(q.pending))
	}
	graph[0].members[0] = "S-1-5-18"
	graph[0].sid = "S-1-5-32-544"
	if got[0].sid != "S-1-5-32-545" || got[0].members[0] != b.sid {
		t.Fatal("graph aliases source memory")
	}
	if !q.releasePending(func() bool { return true }) {
		t.Fatal("cleanup failed")
	}
}
func TestPermissionAliasesCollectorRetainsPartialAndRejectsForeignScope(t *testing.T) {
	for _, kind := range []string{"error", "cancel", "guard", "wrong-kind", "foreign-binding"} {
		t.Run(kind, func(t *testing.T) {
			q, f, b := accountQueryTest(t)
			m := &accountQueryFixtureMemory{f: f, label: "groups", value: accountQueryObservation{kind: accountQueryGroups, groups: []permissionGroup{}}}
			k := &permissionGroupsFixture{memory: m}
			c, _ := newPermissionGroupsCollector(q, k)
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			healthy := true
			switch kind {
			case "error":
				k.err = errors.New("partial")
			case "cancel":
				k.hook = cancel
			case "guard":
				k.hook = func() { healthy = false }
			case "wrong-kind":
				m.value.kind = accountQueryUser
			case "foreign-binding":
				b.intent[0]++
			}
			if _, e := c.collect(ctx, b, func() bool { return healthy }); e == nil {
				t.Fatal("partial became complete")
			}
			want := 1
			if kind == "foreign-binding" {
				want = 0
			}
			if len(q.pending) != want {
				t.Fatal("partial memory lost", len(q.pending))
			}
		})
	}
}
