package guestaccount

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type permissionInspectionFactsFixture struct {
	q         *accountQuery
	f         *accountQueryFixture
	facts     permissionFacts
	rounds    int
	hook      func(int)
	rightsErr error
	extra     []string
}

func (k *permissionInspectionFactsFixture) user4(b binding, _ permissionFactsPhase) (accountQueryMemory, error) {
	k.rounds++
	if k.hook != nil {
		k.hook(k.rounds)
	}
	return &accountQueryFixtureMemory{f: k.f, label: "user4", value: accountQueryObservation{kind: accountQueryUser4, name: b.name, sid: b.sid, flags: k.facts.flags, primary: k.facts.primary, priv: k.facts.priv, authFlags: k.facts.authFlags}}, nil
}
func (k *permissionInspectionFactsFixture) rights(sid string, _ permissionFactsPhase) (accountQueryMemory, error) {
	r := []string{}
	if sid == "S-1-5-32-545" {
		r = []string{"SeChangeNotifyPrivilege", "SeIncreaseWorkingSetPrivilege", "SeTimeZonePrivilege", "SeNetworkLogonRight"}
	}
	if sid == k.q.binding.sid {
		r = append(r, k.extra...)
	}
	return &accountQueryFixtureMemory{f: k.f, label: "rights", value: accountQueryObservation{kind: accountQueryRights, sid: sid, rights: r}}, k.rightsErr
}

type permissionInspectionObjectsFixture struct{}

func (*permissionInspectionObjectsFixture) metadata(o permissionObject, _ *permissionObjectWork, check func() bool) (permissionObjectMetadata, error) {
	if !check() {
		return permissionObjectMetadata{}, ErrDenied
	}
	return permissionObjectMetadata{pid: 91, currentPID: 91, state: 4, tokenType: 1, tokenUser: o.userSID, fileID: o.fileID, volume: o.volume, fileType: 1, links: 1, directory: o.kind == permissionObjectDirectory}, nil
}
func (*permissionInspectionObjectsFixture) security(_ permissionObject, w *permissionObjectWork, check func() bool) (uint32, error) {
	if !check() {
		return 0, ErrDenied
	}
	d := permissionDescriptorFixture()[:40]
	d[34] = 8
	d[36] = 0
	copy(w.buffer, d)
	return uint32(len(d)), nil
}
func (*permissionInspectionObjectsFixture) descriptor(_ []byte, _ permissionObjectDescriptor, check func() bool) error {
	if !check() {
		return ErrDenied
	}
	return nil
}

func permissionInspectionTest(t *testing.T) (*accountQuery, *accountQueryFixture, *permissionInspectionFactsFixture, *permissionGroupsFixture, binding) {
	t.Helper()
	q, f, b := accountQueryTest(t)
	f.role.value.role = 2
	groups := []string{b.machine + "-513", "S-1-1-0", "S-1-2-0", "S-1-5-11", "S-1-5-113", "S-1-5-15", "S-1-5-2", "S-1-5-32-545"}
	p := q.plan
	p.policy = limitedPolicy{known: true, groups: groups, privileges: []string{"SeChangeNotifyPrivilege", "SeIncreaseWorkingSetPrivilege", "SeTimeZonePrivilege"}, logonRights: []string{"SeNetworkLogonRight"}}
	p.binding.policy = policyDigest(p.policy)
	b.policy = p.binding.policy
	q.plan = p
	q.binding = b
	facts := &permissionInspectionFactsFixture{q: q, f: f, facts: permissionFacts{flags: 0x202, primary: 513, priv: 1}}
	aliases := &permissionGroupsFixture{memory: &accountQueryFixtureMemory{f: f, label: "aliases", value: accountQueryObservation{kind: accountQueryGroups, groups: []permissionGroup{{sid: "S-1-5-32-545", members: []string{"S-1-5-11"}}}}}}
	profile := permissionProfile{serviceSID: "S-1-5-80-1-2-3-4-5", objects: []permissionObject{}}
	kinds := []permissionObjectKind{permissionObjectService, permissionObjectProcess, permissionObjectToken, permissionObjectFile, permissionObjectFile, permissionObjectFile, permissionObjectFile, permissionObjectDirectory}
	roles := []permissionRole{roleService, roleSupervisorProcess, roleSupervisorToken, roleConfiguration, roleJournal, roleHostKey, roleArtifact, roleAncestor}
	for i, kind := range kinds {
		profile.objects = append(profile.objects, permissionObject{role: roles[i], kind: kind, handle: uintptr(30 + i), fileID: [16]byte{byte(i + 1)}, volume: 1, userSID: b.service})
	}
	if e := configurePermissionInspection(q, profile, facts, aliases, newPermissionObjectReader(&permissionInspectionObjectsFixture{})); e != nil {
		t.Fatal(e)
	}
	return q, f, facts, aliases, b
}

func TestPermissionInspectionAcceptsDisabledLimitedStagingAndRejectsDrift(t *testing.T) {
	q, _, facts, _, b := permissionInspectionTest(t)
	s, e := q.inspect(testContext(t), b)
	if e != nil || !s.complete || !s.disabled || s.binding != b || s.seal != q.seal || len(s.groups) != 8 || len(s.privileges) != 3 {
		t.Fatalf("限定stagingの正例=%+v %v", s, e)
	}
	facts.hook = func(round int) {
		if round == 4 {
			facts.facts.flags |= 0x10000
		}
	}
	if s, e := q.inspect(testContext(t), b); e == nil || s.complete {
		t.Fatal("前回成功又は途中変化を権限証拠へ再利用", s, e)
	}
}
func TestPermissionInspectionRejectsPrimaryDelegationAndAdminPaths(t *testing.T) {
	for _, name := range []string{"primary", "admin", "delegation", "domain-member", "inherited-admin", "legacy-auth", "rights-error", "dangerous-privilege", "unknown-right", "missing-groups", "cancel-free"} {
		t.Run(name, func(t *testing.T) {
			q, f, facts, aliases, b := permissionInspectionTest(t)
			ctx := testContext(t)
			switch name {
			case "primary":
				facts.facts.primary = 512
			case "admin":
				facts.facts.priv = 2
			case "delegation":
				facts.facts.flags |= 0x80000
			case "domain-member":
				f.role.value.role = 3
			case "inherited-admin":
				aliases.memory.(*accountQueryFixtureMemory).value.groups = append(aliases.memory.(*accountQueryFixtureMemory).value.groups, permissionGroup{sid: "S-1-5-32-544", members: []string{"S-1-1-0"}})
			case "legacy-auth":
				facts.facts.authFlags = 1
			case "rights-error":
				facts.rightsErr = errors.New("synthetic rights failure")
			case "dangerous-privilege":
				facts.extra = []string{"SeShutdownPrivilege"}
			case "unknown-right":
				facts.extra = []string{"SeFutureDelegationPrivilege"}
			case "missing-groups":
				aliases.memory.(*accountQueryFixtureMemory).value.groups = nil
			case "cancel-free":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.releaseHook = func(string) { cancel() }
			}
			if s, e := q.inspect(ctx, b); e == nil || s.complete {
				t.Fatal("不明/過剰権限を受理", s, e)
			}
		})
	}
}

func TestPermissionInspectionConnectsCreatorJournalPrepareAndSIDCleanup(t *testing.T) {
	q, _, _, _, b := permissionInspectionTest(t)
	_, f := newFixture(t)
	f.s.accountSeal = q.seal
	deps := creationDependencies{preflight: creationCheckFixture{f}, create: func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) {
			if !bytes.Equal(in, creationRequest(q.plan.binding)) || f.stored.stage != createAttempt {
				t.Fatal("journal作成意図との結合なし")
			}
			return creationResult(in, b.sid, true, true), nil
		}}, nil
	}, inspect: func(made binding) (ownedAccountInspector, error) {
		if made != b {
			t.Fatal("異なるSID")
		}
		return newCreationQueryOwner(q, false)
	}, cleanup: func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) { return sidResult(in), nil }}, nil
	}}
	a, e := newCreationSource(q.plan, q.seal, deps)
	if e != nil {
		t.Fatal(e)
	}
	f.s.accounts = a
	l, e := newLifecycle(q.plan, f.s)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.Prepare(testContext(t)); e != nil || l.record.stage != preparedDisabled {
		t.Fatal("実collectorからPreparedへ到達しない", e, l.record.stage)
	}
	if e = l.Close(testContext(t)); e != nil || f.stored.stage != deleteComplete {
		t.Fatal("SID清掃へ到達しない", e, f.stored.stage)
	}
	if e = a.Close(testContext(t)); e != nil {
		t.Fatal("query終了未確認", e)
	}
}
