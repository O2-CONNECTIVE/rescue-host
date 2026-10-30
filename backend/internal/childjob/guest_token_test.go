package childjob

import (
	"context"
	"testing"
	"time"
)

type guestTokenMemory struct {
	*inspectionMemory
	facts       guestTokenFacts
	reads       int
	onFacts     func(uintptr)
	targetFacts *guestTokenFacts
}

func (m *guestTokenMemory) tokenFacts(handle uintptr) (guestTokenFacts, error) {
	m.reads++
	if m.onFacts != nil {
		m.onFacts(handle)
	}
	if handle != 20 && m.targetFacts != nil {
		return *m.targetFacts, nil
	}
	return m.facts, nil
}
func guestTokenFixture(t *testing.T) (*Prepared, *Inspection, GuestTokenExpectation, *guestTokenMemory, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	const user = "S-1-5-21-7-8-9-2048"
	expectation, err := NewGuestTokenExpectation(user, []string{"S-1-1-0", "S-1-5-2", "S-1-5-11", "S-1-5-32-545"}, []string{"SeChangeNotifyPrivilege"})
	if err != nil {
		t.Fatal(err)
	}
	facts := guestTokenFacts{user: user, owner: user, primaryGroup: "S-1-5-21-7-8-9-513", kind: 1, authentication: 3, tokenID: 8, modifiedID: 9, session: 1, integrity: 0x2000, elevationType: 1, groups: []guestTokenGroup{{"S-1-1-0", 7}, {"S-1-5-2", 7}, {"S-1-5-11", 7}, {"S-1-5-32-545", 7}}, privileges: []guestTokenPrivilege{{"SeChangeNotifyPrivilege", 3, 1}}}
	api := &guestTokenMemory{inspectionMemory: &inspectionMemory{memoryKernel: &memoryKernel{}}, facts: facts}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, end := context.WithTimeout(context.Background(), time.Second)
		defer end()
		if err := p.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	i, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return p, i, expectation, api, ctx
}
func TestGuestTokenRejectsDenyOnlyAdministratorFromOSObservation(t *testing.T) {
	p, i, e, api, ctx := guestTokenFixture(t)
	api.facts.groups = append(api.facts.groups, guestTokenGroup{"S-1-5-32-544", 0x10})
	if observation, err := p.ObserveGuestToken(ctx, i, e); err == nil || observation != nil {
		t.Fatal("deny-only administrator became accepted guest token evidence")
	}
}

func TestGuestTokenObservationCannotBeCopiedToAnotherReceipt(t *testing.T) {
	p, i, e, _, ctx := guestTokenFixture(t)
	observation, err := p.ObserveGuestToken(ctx, i, e)
	if err != nil {
		t.Fatal(err)
	}
	copy := *observation
	if p.GuestTokenCurrent(i, &copy) {
		t.Fatal("copy of observation became original evidence")
	}
}

func TestGuestTokenObservationIsReadOnlyUntilSingleInspectedCommit(t *testing.T) {
	p, i, e, api, ctx := guestTokenFixture(t)
	observation, err := p.ObserveGuestToken(ctx, i, e)
	if err != nil || !p.GuestTokenCurrent(i, observation) {
		t.Fatal("healthy observation was not retained", err)
	}
	if api.reads != 2 {
		t.Fatal("borrowed source and held target were not independently queried")
	}
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("token observation executed")
		}
	}
	if err := p.CommitInspected(ctx, i); err != nil {
		t.Fatal(err)
	}
	if api.reads != 4 || p.GuestTokenCurrent(i, observation) {
		t.Fatal("commit skipped native requery or observation remained reusable")
	}
	if p.CommitInspected(ctx, i) == nil {
		t.Fatal("observed guest executed twice")
	}
}
func TestGuestTokenRejectsUnsafeObservedFacts(t *testing.T) {
	for _, kind := range []string{"admin-enabled", "admin-disabled", "disabled-privilege", "unknown-group", "limited-elevation", "elevated", "high-integrity", "uiaccess", "restricted", "appcontainer", "wrong-user", "wrong-owner", "unknown-primary", "duplicate-group", "unknown-flags", "unknown-priv-flags", "empty-groups", "empty-privileges", "impersonation", "zero-token-id"} {
		t.Run(kind, func(t *testing.T) {
			p, i, e, api, ctx := guestTokenFixture(t)
			switch kind {
			case "admin-enabled":
				api.facts.groups = append(api.facts.groups, guestTokenGroup{"S-1-5-32-544", 7})
			case "admin-disabled":
				api.facts.groups = append(api.facts.groups, guestTokenGroup{"S-1-5-32-544", 0})
			case "disabled-privilege":
				api.facts.privileges = append(api.facts.privileges, guestTokenPrivilege{"SeDebugPrivilege", 0, 44})
			case "unknown-group":
				api.facts.groups = append(api.facts.groups, guestTokenGroup{"S-1-5-32-555", 7})
			case "limited-elevation":
				api.facts.elevationType = 3
			case "elevated":
				api.facts.elevated = true
			case "high-integrity":
				api.facts.integrity = 0x3000
			case "uiaccess":
				api.facts.uiAccess = true
			case "restricted":
				api.facts.restricted = true
			case "appcontainer":
				api.facts.appContainer = true
			case "wrong-user":
				api.facts.user = "S-1-5-21-7-8-9-2049"
			case "wrong-owner":
				api.facts.owner = "S-1-5-18"
			case "unknown-primary":
				api.facts.primaryGroup = "S-1-5-32-544"
			case "duplicate-group":
				api.facts.groups = append(api.facts.groups, api.facts.groups[0])
			case "unknown-flags":
				api.facts.groups[0].attributes = 0x20000007
			case "unknown-priv-flags":
				api.facts.privileges[0].attributes = 4
			case "empty-groups":
				api.facts.groups = nil
			case "empty-privileges":
				api.facts.privileges = nil
			case "impersonation":
				api.facts.kind = 2
			case "zero-token-id":
				api.facts.tokenID = 0
			}
			if g, err := p.ObserveGuestToken(ctx, i, e); err == nil || g != nil {
				t.Fatal("unsafe native token facts became evidence")
			}
		})
	}
}
func TestGuestTokenRejectsDriftWithoutRefreshingBaseline(t *testing.T) {
	for _, kind := range []string{"id", "modified", "group-attributes", "privileges", "auth", "session"} {
		t.Run(kind, func(t *testing.T) {
			p, i, e, api, ctx := guestTokenFixture(t)
			g, err := p.ObserveGuestToken(ctx, i, e)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "id":
				api.facts.tokenID++
			case "modified":
				api.facts.modifiedID++
			case "group-attributes":
				api.facts.groups[0].attributes = 5
			case "privileges":
				api.facts.privileges[0].attributes = 0
			case "auth":
				api.facts.authentication++
			case "session":
				api.facts.session++
			}
			if err := p.CommitInspected(ctx, i); err == nil {
				t.Fatal("drift was silently rebaselined")
			}
			if p.GuestTokenCurrent(i, g) {
				t.Fatal("failed native reinspection retained current evidence")
			}
			for _, call := range api.calls {
				if call == "resume" {
					t.Fatal("changed native token executed")
				}
			}
		})
	}
}
func TestGuestTokenTargetAndAdmissionCannotBeSubstituted(t *testing.T) {
	p, i, e, api, ctx := guestTokenFixture(t)
	q, j, _, _, _ := guestTokenFixture(t)
	if g, err := q.ObserveGuestToken(ctx, i, e); err == nil || g != nil {
		t.Fatal("another held target used this inspection")
	}
	g, err := p.ObserveGuestToken(ctx, i, e)
	if err != nil {
		t.Fatal(err)
	}
	if q.GuestTokenCurrent(j, g) {
		t.Fatal("observation transferred to another target")
	}
	p.Revoke()
	if p.GuestTokenCurrent(i, g) || p.CommitInspected(ctx, i) == nil {
		t.Fatal("revoked evidence borrowed a fresh call")
	}
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("revoked target executed")
		}
	}
}
func TestGuestTokenSourceAndTargetPrincipalMustMatch(t *testing.T) {
	p, i, e, api, ctx := guestTokenFixture(t)
	target := cloneGuestTokenFacts(api.facts)
	target.authentication++
	api.targetFacts = &target
	if g, err := p.ObserveGuestToken(ctx, i, e); err == nil || g != nil {
		t.Fatal("separately valid principal replaced the source logon")
	}
}
func TestGuestTokenCanceledNativeReadRetainsPartialQuery(t *testing.T) {
	p, i, e, api, _ := guestTokenFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api.onFacts = func(uintptr) { cancel() }
	api.failure = "close-query"
	if _, err := p.ObserveGuestToken(ctx, i, e); err != ErrUnconfirmed {
		t.Fatal("uncertain query cleanup was reported complete", err)
	}
	if p.guestQueryToken == 0 {
		t.Fatal("returned target query reference was lost")
	}
	api.failure = ""
	api.onFacts = nil
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := p.Close(cleanup); err != nil {
		t.Fatal(err)
	}
}

func TestGuestTokenExpectationCannotAuthorizeUnknownOrPrivilegedPolicy(t *testing.T) {
	for _, kind := range []string{"admin-group", "unknown-group", "dangerous-privilege", "nil-groups", "reserved-user", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			user := "S-1-5-21-7-8-9-2048"
			groups := []string{"S-1-1-0", "S-1-5-2", "S-1-5-11", "S-1-5-32-545"}
			privileges := []string{}
			switch kind {
			case "admin-group":
				groups = append(groups, "S-1-5-32-544")
			case "unknown-group":
				groups = append(groups, "S-1-5-32-555")
			case "dangerous-privilege":
				privileges = append(privileges, "SeDebugPrivilege")
			case "nil-groups":
				groups = nil
			case "reserved-user":
				user = "S-1-5-21-7-8-9-500"
			case "duplicate":
				groups = append(groups, groups[0])
			}
			if _, err := NewGuestTokenExpectation(user, groups, privileges); err == nil {
				t.Fatal("unreviewed expectation enabled token policy")
			}
		})
	}
}
func TestGuestTokenObservationDoesNotOutliveOriginalAdmission(t *testing.T) {
	p, i, e, _, ctx := guestTokenFixture(t)
	admission := &inspectionDeadlineContext{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
	p.lease = admission // private pre-acquisition context fixture only
	g, err := p.ObserveGuestToken(ctx, i, e)
	if err != nil {
		t.Fatal(err)
	}
	admission.deadline = time.Now().Add(-time.Second)
	if p.GuestTokenCurrent(i, g) || p.CommitInspected(ctx, i) == nil {
		t.Fatal("stale token observation used fresh call context")
	}
}
