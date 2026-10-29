package guestaccount

import "testing"

func TestExplicitExistingServiceIdentityDoesNotRequireThreeAccounts(t *testing.T) {
	p := testPlan(t)
	for _, service := range []string{p.binding.operator, "S-1-5-80-1-2-3-4-5", "S-1-5-19", "S-1-5-20"} {
		in := ProtectedInputs{sessionEpoch: p.binding.epoch, intentID: p.binding.intent, generation: 1, machineSID: p.binding.machine, operatorSID: p.binding.operator, serviceUserSID: service, policy: p.policy}
		if _, err := NewPlan(in); err != nil {
			t.Fatalf("明示された既存service主体 %s: %v", service, err)
		}
	}
}

func TestRestrictedPlanRejectsObviousAdministrativeAllowlist(t *testing.T) {
	p := testPlan(t)
	for _, name := range []string{"S-1-5-32-544", "SeDebugPrivilege", "SeTcbPrivilege", "SeCreateTokenPrivilege", "SeAssignPrimaryTokenPrivilege", "SeImpersonatePrivilege", "SeLoadDriverPrivilege", "SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege", "sedebugprivilege", "SeShutdownPrivilege", "SeRemoteShutdownPrivilege", "SeSystemtimePrivilege", "SeManageVolumePrivilege", "SeSecurityPrivilege", "SeRelabelPrivilege", "SeSystemEnvironmentPrivilege"} {
		t.Run(name, func(t *testing.T) {
			policy := copyPolicy(p.policy)
			if name == "S-1-5-32-544" {
				policy.groups = append(policy.groups, name)
			} else {
				policy.privileges = append(policy.privileges, name)
			}
			in := ProtectedInputs{sessionEpoch: p.binding.epoch, intentID: p.binding.intent, generation: 1, machineSID: p.binding.machine, operatorSID: p.binding.operator, serviceUserSID: p.binding.service, policy: policy}
			if _, err := NewPlan(in); err == nil {
				t.Fatal("明白な管理権限をallowlistへ許可")
			}
		})
	}
}

func TestPlanRejectsMissingProtectedIdentityOrPolicy(t *testing.T) {
	p := testPlan(t)
	for _, change := range []func(*ProtectedInputs){
		func(i *ProtectedInputs) { i.sessionEpoch = [32]byte{} }, func(i *ProtectedInputs) { i.intentID = [32]byte{} }, func(i *ProtectedInputs) { i.generation = 0 }, func(i *ProtectedInputs) { i.machineSID = "S-1-5-21-1-2" }, func(i *ProtectedInputs) { i.operatorSID = "S-1-5-18" }, func(i *ProtectedInputs) { i.serviceUserSID = "" }, func(i *ProtectedInputs) { i.policy.known = false }, func(i *ProtectedInputs) { i.policy.groups = nil }, func(i *ProtectedInputs) { i.policy.privileges = nil }, func(i *ProtectedInputs) { i.policy.logonRights = nil },
	} {
		in := ProtectedInputs{sessionEpoch: p.binding.epoch, intentID: p.binding.intent, generation: 1, machineSID: p.binding.machine, operatorSID: p.binding.operator, serviceUserSID: p.binding.service, policy: copyPolicy(p.policy)}
		change(&in)
		if _, err := NewPlan(in); err == nil {
			t.Fatal("未設定protected inputを受理")
		}
	}
	if len(p.binding.name) != 20 || p.binding.name[:4] != "rhg_" {
		t.Fatalf("内部名20字制約=%s", p.binding.name)
	}
}
