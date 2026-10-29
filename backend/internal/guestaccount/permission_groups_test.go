package guestaccount

import (
	"testing"
)

func TestPermissionClosureIncludesPotentialIdentityAndRejectsIndirectAdmin(t *testing.T) {
	p := testPlan(t)
	b := p.binding
	b.sid = b.machine + "-2002"
	groups := []permissionGroup{{sid: "S-1-5-32-545", members: []string{"S-1-5-11"}}, {sid: "S-1-5-32-544", members: []string{b.machine + "-1001"}}}
	got, err := permissionClosure(b, groups, "S-1-5-2")
	if err != nil || !containsPermissionSID(got, "S-1-5-32-545") || !containsPermissionSID(got, b.machine+"-513") {
		t.Fatal(got, err)
	}
	groups[1].members = append(groups[1].members, "S-1-1-0")
	if _, err = permissionClosure(b, groups, "S-1-5-2"); err == nil {
		t.Fatal("Everyone経由のadminを許可")
	}
}
