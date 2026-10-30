package childjob

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

const protectedTestService = "S-1-5-80-1-2-3-4-5"
const protectedTestGuest = "S-1-5-21-1-2-3-1001"

func TestProtectedConfigRejectsUnboundPrincipalsBeforePlatformAccess(t *testing.T) {
	for _, pair := range [][2]string{{"", protectedTestGuest}, {"S-1-5-18", protectedTestGuest}, {protectedTestService, "S-1-5-32-544"}, {"S-1-5-80-01-2-3-4-5", protectedTestGuest}} {
		if _, err := NewProtectedGuestConfig(pair[0], pair[1]); err == nil {
			t.Fatal("保護主体の未設定・非canonical入力を受理しました")
		}
	}
	config, err := NewProtectedGuestConfig(protectedTestService, protectedTestGuest)
	if err != nil {
		t.Fatal(err)
	}
	spec := approvedSpec()
	spec.Protection = &config
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if child, err := Start(ctx, spec); child != nil || err != ErrConfig {
		t.Fatalf("保護観測を迂回するStartを受理しました: %v", err)
	}
}

type protectedMemory struct {
	*inspectionMemory
	unsafeHandle  uintptr
	queryFailure  bool
	securityCalls []uintptr
	protectedJobs int
}

func (m *protectedMemory) newProtectedJob(c ProtectedGuestConfig) (uintptr, error) {
	if !c.valid {
		panic("未設定の保護設定")
	}
	m.protectedJobs++
	return m.memoryKernel.newJob()
}

func (m *protectedMemory) createSuspended(s Spec) (uintptr, uintptr, error) {
	if s.Protection == nil || !s.Protection.valid || s.Protection.service != protectedTestService {
		return 0, 0, ErrConfig
	}
	return m.memoryKernel.createSuspended(s)
}

func (m *protectedMemory) tokenFacts(uintptr) (guestTokenFacts, error) {
	return guestTokenFacts{user: protectedTestGuest, owner: protectedTestGuest, primaryGroup: "S-1-5-21-1-2-3-513", kind: 1, authentication: 3, tokenID: 8, modifiedID: 9, session: 1, integrity: 0x2000, elevationType: 1, groups: []guestTokenGroup{{"S-1-1-0", 7}, {"S-1-5-2", 7}, {"S-1-5-11", 7}, {"S-1-5-32-545", 7}}, privileges: []guestTokenPrivilege{{"SeChangeNotifyPrivilege", 3, 1}}}, nil
}
func observeProtectedToken(t *testing.T, p *Prepared, i *Inspection, ctx context.Context) {
	t.Helper()
	e, err := NewGuestTokenExpectation(protectedTestGuest, []string{"S-1-1-0", "S-1-5-2", "S-1-5-11", "S-1-5-32-545"}, []string{"SeChangeNotifyPrivilege"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ObserveGuestToken(ctx, i, e); err != nil {
		t.Fatal(err)
	}
}
func (m *protectedMemory) objectSecurity(h uintptr) (protectedSecurity, error) {
	m.securityCalls = append(m.securityCalls, h)
	if m.queryFailure {
		return protectedSecurity{}, errors.New("query failed")
	}
	f := protectedSecurity{owner: "S-1-5-18", present: true, protected: true, entries: []protectedACE{{sid: "S-1-5-18", mask: protectedMask(objectKind(h))}, {sid: protectedTestService, mask: protectedMask(objectKind(h))}}}
	if h == m.unsafeHandle {
		f.owner = protectedTestGuest
	}
	return f, nil
}

// The fake's numeric handles are fixed by the existing lifecycle fixture.
func objectKind(h uintptr) protectedKind {
	switch h {
	case 2:
		return protectedJob
	case 3:
		return protectedProcess
	case 4:
		return protectedThread
	default:
		return protectedToken
	}
}
func protectedSpec(t *testing.T) Spec {
	t.Helper()
	c, err := NewProtectedGuestConfig(protectedTestService, protectedTestGuest)
	if err != nil {
		t.Fatal(err)
	}
	s := approvedSpec()
	s.Protection = &c
	return s
}

func TestProtectedPrepareUsesProtectedCreationAndSameHeldObjects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}}
	p, err := prepareWithKernel(ctx, protectedSpec(t), m)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(ctx)
	if m.protectedJobs != 1 {
		t.Fatal("既定DACLのJob経路へfallbackしました")
	}
	i, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observeProtectedToken(t, p, i, ctx)
	g, err := p.ObserveProtectedGuest(ctx, i)
	if err != nil || !p.ProtectedGuestCurrent(i, g) {
		t.Fatal("同じ対象の保護観測がありません", err)
	}
	seen := map[uintptr]bool{}
	for _, h := range m.securityCalls {
		seen[h] = true
	}
	for _, h := range []uintptr{20, 2, 3, 4, 6} {
		if !seen[h] {
			t.Fatalf("同じhandleのSD未照会: %d", h)
		}
	}
	if err := p.CommitInspected(ctx, i); err != nil {
		t.Fatal(err)
	}
	if p.ProtectedGuestCurrent(i, g) {
		t.Fatal("消費後の観測をcurrentとしました")
	}
}

func TestProtectedCommitRequiresObservationAndRechecksEveryObject(t *testing.T) {
	for _, changed := range []uintptr{0, 20, 2, 3, 4, 7} {
		t.Run(strconv.FormatUint(uint64(changed), 10), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}}
			p, err := prepareWithKernel(ctx, protectedSpec(t), m)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close(ctx)
			if p.Commit(ctx) == nil {
				t.Fatal("旧Commitで観測を迂回しました")
			}
			i, err := p.Inspect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			observeProtectedToken(t, p, i, ctx)
			if changed != 0 {
				if _, err := p.ObserveProtectedGuest(ctx, i); err != nil {
					t.Fatal(err)
				}
				m.unsafeHandle = changed
			}
			if err := p.CommitInspected(ctx, i); err == nil {
				t.Fatal("保護観測なし/変更済みSDで実行しました")
			}
			for _, call := range m.calls {
				if call == "resume" {
					t.Fatal("拒否後resumeしました")
				}
			}
		})
	}
}

func TestProtectedObservationRejectsCopiedForeignAndRevokedProof(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	newOwner := func() (*Prepared, *Inspection, *ProtectedGuestObservation) {
		m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}}
		p, err := prepareWithKernel(ctx, protectedSpec(t), m)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close(ctx) })
		i, err := p.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		observeProtectedToken(t, p, i, ctx)
		g, err := p.ObserveProtectedGuest(ctx, i)
		if err != nil {
			t.Fatal(err)
		}
		return p, i, g
	}
	p, i, g := newOwner()
	q, j, h := newOwner()
	copyG := *g
	copyI := *i
	if !g.MatchesGuestSID(protectedTestGuest) || g.MatchesGuestSID("S-1-5-21-1-2-3-1002") || copyG.MatchesGuestSID(protectedTestGuest) {
		t.Fatal("固定したguestとの比較がcopy/別guestを受理しました")
	}
	if p.ProtectedGuestCurrent(i, &copyG) || p.ProtectedGuestCurrent(&copyI, g) || q.ProtectedGuestCurrent(i, g) || p.ProtectedGuestCurrent(j, h) {
		t.Fatal("copy/foreign観測を受理しました")
	}
	if q.CommitInspected(ctx, i) == nil {
		t.Fatal("別Preparedの観測で実行しました")
	}
	if err := p.Revoke(); err != nil {
		t.Fatal(err)
	}
	if p.ProtectedGuestCurrent(i, g) || g.MatchesGuestSID(protectedTestGuest) || p.CommitInspected(ctx, i) == nil {
		t.Fatal("失効済み観測で実行しました")
	}
}

func TestProtectedSecurityRejectsUnprotectedNullAndDangerousACL(t *testing.T) {
	c, _ := NewProtectedGuestConfig(protectedTestService, protectedTestGuest)
	for _, kind := range []protectedKind{protectedToken, protectedProcess, protectedThread, protectedJob} {
		good := func() protectedSecurity {
			return protectedSecurity{owner: "S-1-5-18", present: true, protected: true, entries: []protectedACE{{sid: "S-1-5-18", mask: protectedMask(kind)}, {sid: protectedTestService, mask: protectedMask(kind)}}}
		}
		if !c.accepts(good(), kind) {
			t.Fatal("固定descriptorを拒否しました")
		}
		for _, mutate := range []func(*protectedSecurity){
			func(f *protectedSecurity) { f.owner = protectedTestGuest },
			func(f *protectedSecurity) { f.owner = protectedTestService },
			func(f *protectedSecurity) { f.present = false },
			func(f *protectedSecurity) { f.protected = false },
			func(f *protectedSecurity) { f.entries = nil },
			func(f *protectedSecurity) { f.entries = []protectedACE{} },
			func(f *protectedSecurity) { f.entries[1].sid = protectedTestGuest },
			func(f *protectedSecurity) { f.entries[1].sid = "S-1-1-0" },
			func(f *protectedSecurity) { f.entries[1].sid = "S-1-5-18" },
			func(f *protectedSecurity) { f.entries[1].kind = 1 },
			func(f *protectedSecurity) { f.entries[1].kind = 5 },
			func(f *protectedSecurity) { f.entries[1].flags = 0x10 },
			func(f *protectedSecurity) { f.entries[1].flags = 0x08 },
			func(f *protectedSecurity) { f.entries[1].mask = 0x10000000 },
			func(f *protectedSecurity) { f.entries[1].mask = 0 },
			func(f *protectedSecurity) {
				f.entries = append(f.entries, protectedACE{sid: protectedTestGuest, mask: 0x40000})
			},
		} {
			f := good()
			mutate(&f)
			if c.accepts(f, kind) {
				t.Fatal("危険・未知・継承ACE/ownerを受理しました")
			}
		}
	}
}

func TestProtectedFailureRetainsCleanupAndNeverFallsBack(t *testing.T) {
	for _, bad := range []uintptr{20, 2, 3, 4} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}, unsafeHandle: bad}
		p, err := prepareWithKernel(ctx, protectedSpec(t), m)
		if err == nil {
			t.Fatal("不正SDのままprepareしました")
		}
		if p != nil {
			_ = p.Close(ctx)
		}
		for _, call := range m.calls {
			if call == "resume" {
				t.Fatal("不正SDで実行しました")
			}
		}
		if bad == 20 && m.protectedJobs != 0 {
			t.Fatal("source token検査前にJobを取得しました")
		}
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	if p, err := prepareWithKernel(ctx, protectedSpec(t), api); err == nil || p != nil {
		t.Fatal("保護未対応providerにfallbackしました")
	}
}

func TestProtectedConfigSnapshotSurvivesCallerMutation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := protectedSpec(t)
	m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}}
	p, err := prepareWithKernel(ctx, s, m)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(ctx)
	*s.Protection = ProtectedGuestConfig{}
	i, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observeProtectedToken(t, p, i, ctx)
	if _, err := p.ObserveProtectedGuest(ctx, i); err != nil {
		t.Fatal("callerの変更が固定設定へ波及しました", err)
	}
	if err := p.CommitInspected(ctx, i); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedObservationRequiresSameGuestTokenCandidate(t *testing.T) {
	for _, wrong := range []string{"missing", "other-guest"} {
		t.Run(wrong, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s := protectedSpec(t)
			if wrong == "other-guest" {
				c, err := NewProtectedGuestConfig(protectedTestService, "S-1-5-21-1-2-3-1002")
				if err != nil {
					t.Fatal(err)
				}
				s.Protection = &c
			}
			m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}}
			p, err := prepareWithKernel(ctx, s, m)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close(ctx)
			i, err := p.Inspect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if wrong != "missing" {
				observeProtectedToken(t, p, i, ctx)
			}
			if g, err := p.ObserveProtectedGuest(ctx, i); err == nil || g != nil {
				t.Fatal("guest token未観測/別guestの保護candidateを受理しました")
			}
		})
	}
}

func TestProtectedObjectMasksStayCanonical(t *testing.T) {
	for _, v := range []struct {
		kind protectedKind
		mask uint32
	}{{protectedToken, 0x000f01ff}, {protectedProcess, 0x001fffff}, {protectedThread, 0x001fffff}, {protectedJob, 0x001f003f}} {
		if protectedMask(v.kind) != v.mask {
			t.Fatal("作成/検査maskがcanonical定義から変わりました")
		}
	}
}

func TestProtectedInspectionFailureKeepsUnconfirmedTargetOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{failure: "close-process"}}, unsafeHandle: 3}
	p, err := prepareWithKernel(ctx, protectedSpec(t), m)
	if err != ErrUnconfirmed || p == nil || p.child.process != 3 {
		t.Fatal("SD拒否後の未回収processを捨てました", err)
	}
	m.failure = ""
	if p.Commit(ctx) == nil {
		t.Fatal("失敗後に再開しました")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if p.child.process != 0 {
		t.Fatal("同じ参照を回収できませんでした")
	}
}

func TestProtectedCommitRejectsReplacedTokenObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &protectedMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}}
	p, err := prepareWithKernel(ctx, protectedSpec(t), m)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(ctx)
	i, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observeProtectedToken(t, p, i, ctx)
	g, err := p.ObserveProtectedGuest(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	copyToken := *p.guestObservation
	p.guestObservation = &copyToken // Model accidental internal handoff of a copied receipt.
	if p.ProtectedGuestCurrent(i, g) || p.CommitInspected(ctx, i) == nil {
		t.Fatal("置換したtoken観測を同じ証拠としました")
	}
	for _, call := range m.calls {
		if call == "resume" {
			t.Fatal("置換した観測で実行しました")
		}
	}
}
