package guestaccount

import (
	"reflect"
	"testing"
)

type permissionFactsKernelFixture struct {
	user, assigned       accountQueryMemory
	userErr, rightsErr   error
	calls                []string
	lastSID              string
	inflight             accountQueryMemory
	userHook, rightsHook func()
}

func (k *permissionFactsKernelFixture) user4(b binding, _ permissionFactsPhase) (accountQueryMemory, error) {
	k.calls = append(k.calls, "user4:"+b.name)
	k.inflight = k.user
	if k.userHook != nil {
		k.userHook()
	}
	return k.user, k.userErr
}
func (k *permissionFactsKernelFixture) rights(sid string, _ permissionFactsPhase) (accountQueryMemory, error) {
	k.calls = append(k.calls, "rights:"+sid)
	k.lastSID = sid
	k.inflight = k.assigned
	if k.rightsHook != nil {
		k.rightsHook()
	}
	return k.assigned, k.rightsErr
}

func TestPermissionFactsRetainsBothReadsOnOriginalQueryAndCopiesRights(t *testing.T) {
	q, f, b := accountQueryTest(t)
	rights := []string{"SeNetworkLogonRight"}
	k := &permissionFactsKernelFixture{user: &accountQueryFixtureMemory{f: f, label: "user4", value: accountQueryObservation{kind: accountQueryUser4, name: b.name, sid: b.sid, flags: 0x202, primary: 777, priv: 2, authFlags: 8}}, assigned: &accountQueryFixtureMemory{f: f, label: "rights", value: accountQueryObservation{kind: accountQueryRights, sid: "S-1-1-0", rights: rights}}}
	c, e := newPermissionFactsCollector(q, k)
	if e != nil {
		t.Fatal(e)
	}
	facts, e := c.user(testContext(t), b, func() bool { return true })
	if e != nil || facts.primary != 777 || facts.flags != 0x202 || facts.priv != 2 || facts.authFlags != 8 {
		t.Fatal("raw user4 facts were defaulted or not bound", facts, e)
	}
	got, e := c.assigned(testContext(t), "S-1-1-0", func() bool { return true })
	if e != nil || !reflect.DeepEqual(got, rights) {
		t.Fatal("explicit SID rights lost", got, e)
	}
	rights[0] = "SeDebugPrivilege"
	if got[0] != "SeNetworkLogonRight" {
		t.Fatal("native list was not copied")
	}
	if len(q.pending) != 2 || k.lastSID != "S-1-1-0" {
		t.Fatal("reads not retained on same query")
	}
	if !q.releasePending(func() bool { return true }) {
		t.Fatal("same-scope cleanup failed")
	}
}
