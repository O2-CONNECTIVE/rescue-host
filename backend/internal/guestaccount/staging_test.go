package guestaccount

import (
	"context"
	"testing"
)

type stagingScopeFixture struct {
	checks, closes int
	err, closeErr  error
}

func (s *stagingScopeFixture) Recheck(context.Context) error { s.checks++; return s.err }
func (s *stagingScopeFixture) Close(context.Context) error   { s.closes++; return s.closeErr }

type stagingResourceFixture struct {
	handle uintptr
	file   stagingFile
	closes int
	err    error
}

func (r *stagingResourceFixture) value() uintptr              { return r.handle }
func (r *stagingResourceFixture) close(context.Context) error { r.closes++; return r.err }

type stagingFixture struct {
	scope          *stagingScopeFixture
	refs           []*stagingResourceFixture
	principal      accountQueryPrincipal
	thread         uint32
	journal        *journalKernelFixture
	profile        permissionProfile
	accounts       int
	openHook       func(permissionObjectKind)
	nonterminal    bool
	principalCalls int
	query          *accountQueryFixture
	createHook     func()
}
type stagingObjectKernelFixture struct {
	permissionInspectionObjectsFixture
}

func (k *stagingObjectKernelFixture) metadata(o permissionObject, w *permissionObjectWork, c func() bool) (permissionObjectMetadata, error) {
	m, e := k.permissionInspectionObjectsFixture.metadata(o, w, c)
	if o.kind == permissionObjectPipe {
		m.fileType = 3
	}
	return m, e
}
func stagingTest(t *testing.T) (Plan, stagingProfile, *fixture, *stagingFixture, stagingOps) {
	t.Helper()
	q, query, _, _, b := permissionInspectionTest(t)
	p := q.plan
	// 合成したpolicyも通常のPlan入口を通し、private valid flagで正例を作らない。
	var err error
	p, err = NewPlan(ProtectedInputs{sessionEpoch: p.binding.epoch, intentID: p.binding.intent, generation: p.binding.generation, machineSID: p.binding.machine, operatorSID: p.binding.operator, serviceUserSID: p.binding.service, policy: p.policy})
	if err != nil {
		t.Fatal(err)
	}
	_, c := stagingProfileFixture(t)
	_, f := newFixture(t)
	k := &stagingFixture{scope: &stagingScopeFixture{}, principal: accountQueryPrincipal{user: p.binding.service, kind: 1, tokenID: 1, authenticationID: 2, modifiedID: 3}, thread: 7}
	k.query = query
	ops := stagingOps{thread: func() uint32 { return k.thread }, principal: func() (accountQueryPrincipal, error) { k.principalCalls++; return k.principal, nil }, scope: func(context.Context) (stagingScope, error) { return k.scope, nil }, launch: func(context.Context, accountQueryPrincipal, func() bool) error { return nil }, open: func(_ context.Context, kind permissionObjectKind, file stagingFile, _ func() bool) (stagingResource, error) {
		r := &stagingResourceFixture{handle: uintptr(100 + len(k.refs)), file: file}
		k.refs = append(k.refs, r)
		if k.openHook != nil {
			k.openHook(kind)
		}
		return r, nil
	}, inspect: func(r stagingResource, _ func() bool) (journalFileInfo, error) {
		f := r.(*stagingResourceFixture).file
		return journalFileInfo{identity: f.identity, kind: 1, directory: f.directory, links: 1, protected: true}, nil
	}, journal: func(ctx context.Context, p Plan, root *journalRootCapability) (*journalBackend, error) {
		k.journal = &journalKernelFixture{root: *root, openHandle: 900, data: journalHeader(root.installation)}
		j, e := newJournalBackend(ctx, p, root, k.journal)
		if e == nil && k.nonterminal {
			_, e = j.reserve(ctx, record{p.binding, reserved})
		}
		return j, e
	}, accounts: func(p Plan, s *seal, profile permissionProfile) (*creationSource, error) {
		k.accounts++
		k.profile = profile
		q.seal = s
		q.permission.profile = profile
		q.permission.objects = newPermissionObjectReader(&stagingObjectKernelFixture{})
		pre, e := newCreationCheck(p, s, k.query)
		if e != nil {
			return nil, e
		}
		deps := creationDependencies{preflight: pre, create: func() (sidExecutor, error) {
			return &sidTestExecutor{run: func(in []byte) ([]byte, error) {
				if k.journal.writes < 2 {
					t.Fatal("journal確定より先のaccount作成")
				}
				if k.createHook != nil {
					k.createHook()
				}
				return creationResult(in, b.sid, true, true), nil
			}}, nil
		}, inspect: func(made binding) (ownedAccountInspector, error) {
			if made != b {
				t.Fatal("別SID")
			}
			return newCreationQueryOwner(q, false)
		}, cleanup: func() (sidExecutor, error) {
			return &sidTestExecutor{run: func(in []byte) ([]byte, error) { return sidResult(in), nil }}, nil
		}}
		return newCreationSource(p, s, deps)
	}}
	return p, c, f, k, ops
}

func TestStagingCleanupRetryDoesNotPermanentlyCloseAccountSource(t *testing.T) {
	p, c, f, k, ops := stagingTest(t)
	o, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
	if e != nil {
		t.Fatal(e)
	}
	if e = o.Prepare(testContext(t)); e != nil {
		t.Fatal(e)
	}
	f.quiesceEntered = make(chan struct{})
	f.quiesceRelease = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- o.Close(testContext(t)) }()
	<-f.quiesceEntered
	k.query.userErr = ErrDenied
	close(f.quiesceRelease)
	if e = <-done; e == nil {
		t.Fatal("未確認snapshotで清掃成功")
	}
	f.quiesceEntered = nil
	f.quiesceRelease = nil
	k.query.userErr = nil
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal("同じsourceの正規清掃再試行を破壊", e)
	}
}

func TestStagingStopDuringCreationCannotFinishPrepared(t *testing.T) {
	p, c, f, k, ops := stagingTest(t)
	entered, release := make(chan struct{}), make(chan struct{})
	k.createHook = func() { close(entered); <-release }
	o, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- o.Prepare(testContext(t)) }()
	<-entered
	if e = o.Close(testContext(t)); e == nil {
		t.Fatal("実call未終了を回収ack")
	}
	close(release)
	if e = <-done; e == nil || o.lifecycle.state == Prepared {
		t.Fatal("停止要求後にPreparedへ進行", e, o.lifecycle.state)
	}
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal("停止後の所有SID回収に失敗", e)
	}
}

func TestStagingUnknownQueryCannotRestartOuterNativeChecks(t *testing.T) {
	p, c, f, k, ops := stagingTest(t)
	o, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
	if e != nil {
		t.Fatal(e)
	}
	checks := k.scope.checks
	k.query.userHook = func() { k.query.principalValue.modifiedID++ }
	if e = o.Prepare(testContext(t)); e == nil {
		t.Fatal("unknown queryを受理")
	}
	if k.scope.checks != checks+1 {
		t.Fatal("unknown後のscope再照会")
	}
	calls := k.principalCalls
	checks = k.scope.checks
	_ = o.Prepare(testContext(t))
	_ = o.Close(testContext(t))
	if k.principalCalls != calls || k.scope.checks != checks || k.scope.closes != 0 || k.journal.closes != 0 {
		t.Fatal("unknown後にnative再開/解放")
	}
	for _, r := range k.refs {
		if r.closes != 0 {
			t.Fatal("unknown queryの借用対象を解放")
		}
	}
}
func TestStagingOwnsFixedObjectsThroughJournalCreatorAndAllCleanup(t *testing.T) {
	p, c, f, k, ops := stagingTest(t)
	owner, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range k.profile.objects {
		if o.kind == permissionObjectPipe || o.role == roleControlPipe {
			t.Fatal("要求専用pipeを長期session検査へ保持")
		}
		if o.role == roleJournal && o.handle != owner.journal.file {
			t.Fatal("journalを再openしたhandleへ差替え")
		}
	}
	if e = owner.Prepare(testContext(t)); e != nil || owner.lifecycle.state != Prepared {
		t.Fatal("Preparedへ到達せず", e)
	}
	if e = owner.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	if !owner.closed || k.scope.closes != 1 || k.journal.closes != 1 {
		t.Fatal("outer資源のClose未確認")
	}
	for _, r := range k.refs {
		if r.closes != 1 {
			t.Fatal("保持資源の回収なし")
		}
	}
	calls := k.principalCalls
	if e = owner.Close(testContext(t)); e != nil || k.principalCalls != calls {
		t.Fatal("Close ack後に元scopeへ再入", e)
	}
}
func TestStagingRejectsUnfinishedPriorJournalBeforeNewAccountSource(t *testing.T) {
	p, c, f, k, ops := stagingTest(t)
	k.nonterminal = true
	owner, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
	if e != ErrManual || owner == nil || k.accounts != 0 {
		t.Fatal("未終端の旧journalから新しいsessionを構成", e, k.accounts)
	}
	if e = owner.Close(testContext(t)); e != nil {
		t.Fatal("開始前の保持物回収なし", e)
	}
}
