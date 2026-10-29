package guestaccount

import "testing"

func TestPermissionObjectsRejectImplicitOwnerAndUnsafeReadOrDelegation(t *testing.T) {
	p := testPlan(t)
	b := p.binding
	b.sid = b.machine + "-2002"
	s := permissionObjectSnapshot{role: roleSupervisorProcess, kind: permissionObjectProcess, owner: b.service, acl: []journalACE{{mask: 0x120000, sid: "S-1-1-0"}}}
	if !permissionObjectSafe(b, "S-1-5-80-1-2-3-4-5", s) {
		t.Fatal("安全な同期/metadata読取を拒否")
	}
	if permissionObjectSafe(b, b.machine+"-513", s) {
		t.Fatal("guest閉包に属するprimaryをservice SIDとして信頼")
	}
	s.owner = b.sid
	if permissionObjectSafe(b, "S-1-5-80-1-2-3-4-5", s) {
		t.Fatal("ownerの暗黙WRITE_DACを許可")
	}
	s.owner = b.service
	s.acl[0].mask = 0x10
	if permissionObjectSafe(b, "S-1-5-80-1-2-3-4-5", s) {
		t.Fatal("process memory readを許可")
	}
	s.role = roleHostKey
	s.kind = permissionObjectFile
	s.acl[0].mask = 1
	if permissionObjectSafe(b, "S-1-5-80-1-2-3-4-5", s) {
		t.Fatal("秘密鍵data読取を許可")
	}
	s.role = roleConfiguration
	s.acl[0].mask = 0x120089
	if !permissionObjectSafe(b, "S-1-5-80-1-2-3-4-5", s) {
		t.Fatal("非秘密設定の読取を拒否")
	}
	s.acl[0].mask = 0x40000000
	if permissionObjectSafe(b, "S-1-5-80-1-2-3-4-5", s) {
		t.Fatal("generic writeを許可")
	}
}
