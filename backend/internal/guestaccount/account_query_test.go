package guestaccount

import (
	"context"
	"reflect"
	"testing"
)

type accountQueryFixture struct {
	trace                                     []string
	principalValue                            accountQueryPrincipal
	role, domain, user                        *accountQueryFixtureMemory
	userHook                                  func()
	releaseHook                               func(string)
	principalErr, roleErr, domainErr, userErr error
	principalPanic                            bool
	principalCalls                            int
	inflight                                  accountQueryMemory
	thread                                    uint32
	entered, unblock                          chan struct{}
}
type accountQueryFixtureMemory struct {
	f               *accountQueryFixture
	label           string
	value           accountQueryObservation
	err, releaseErr error
	releasePanic    bool
	decodePanic     bool
	nativeParts     int
}

func (f *accountQueryFixture) threadID() uint32 { return f.thread }
func (f *accountQueryFixture) principal() (accountQueryPrincipal, error) {
	f.principalCalls++
	if f.principalPanic {
		panic("synthetic principal query")
	}
	return f.principalValue, f.principalErr
}
func (f *accountQueryFixture) localRole() (accountQueryMemory, error) {
	f.trace = append(f.trace, "role")
	if f.role == nil {
		return nil, f.roleErr
	}
	m := *f.role
	return &m, f.roleErr
}
func (f *accountQueryFixture) localDomain() (accountQueryMemory, error) {
	f.trace = append(f.trace, "domain")
	if f.domain == nil {
		return nil, f.domainErr
	}
	m := *f.domain
	return &m, f.domainErr
}
func (f *accountQueryFixture) user23(name string) (accountQueryMemory, error) {
	f.trace = append(f.trace, "user:"+name)
	if f.entered != nil {
		close(f.entered)
		<-f.unblock
	}
	if f.user == nil {
		return nil, f.userErr
	}
	m := *f.user
	f.inflight = &m
	if f.userHook != nil {
		f.userHook()
	}
	return &m, f.userErr
}
func (m *accountQueryFixtureMemory) decode() (accountQueryObservation, error) {
	m.f.trace = append(m.f.trace, "decode:"+m.label)
	if m.decodePanic {
		panic("synthetic decode")
	}
	return m.value, m.err
}

func (m *accountQueryFixtureMemory) release(scopeGuard func() bool) error {
	parts := m.nativeParts
	if parts == 0 {
		parts = 1
	}
	for i := 0; i < parts; i++ {
		if scopeGuard == nil || !scopeGuard() {
			return ErrUnknown
		}
		label := m.label
		if i > 0 {
			label += ":next"
		}
		m.f.trace = append(m.f.trace, "free:"+label)
		if m.releasePanic {
			panic("synthetic free")
		}
		if m.f.releaseHook != nil {
			m.f.releaseHook(m.label)
		}
	}
	return m.releaseErr
}
func accountQueryTest(t *testing.T) (*accountQuery, *accountQueryFixture, binding) {
	t.Helper()
	p := testPlan(t)
	b := p.binding
	b.sid = b.machine + "-1002"
	f := &accountQueryFixture{thread: 7, principalValue: accountQueryPrincipal{user: b.service, kind: 1, tokenID: 1, authenticationID: 2, modifiedID: 3}}
	f.role = &accountQueryFixtureMemory{f: f, label: "role", value: accountQueryObservation{kind: accountQueryRole, role: 1}}
	f.domain = &accountQueryFixtureMemory{f: f, label: "domain", value: accountQueryObservation{kind: accountQueryDomain, sid: b.machine}}
	f.user = &accountQueryFixtureMemory{f: f, label: "user", value: accountQueryObservation{kind: accountQueryUser, name: b.name, sid: b.sid, flags: 0x202}}
	q, err := newAccountQuery(p, b, &seal{8}, f)
	if err != nil {
		t.Fatal(err)
	}
	return q, f, b
}
func TestAccountQueryRechecksMachineAfterNameLookup(t *testing.T) {
	q, f, b := accountQueryTest(t)
	f.userHook = func() { f.domain.value.sid = "S-1-5-21-40-50-60" }
	if _, err := q.query(testContext(t), b); err == nil {
		t.Fatal("照会中のlocal machine変化を受理")
	}
}
func TestAccountQueryCancellationDuringFreeDiscardsDiagnostic(t *testing.T) {
	q, f, b := accountQueryTest(t)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	f.releaseHook = func(string) { cancel() }
	if d, err := q.query(ctx, b); err == nil || d.sid != "" {
		t.Fatalf("解放中cancel後の診断受理=%+v %v", d, err)
	}
}
func TestAccountQueryOnlyDiagnosesAlreadyOwnedLocalSID(t *testing.T) {
	q, f, b := accountQueryTest(t)
	d, err := q.query(testContext(t), b)
	if err != nil || d.binding != b || d.sid != "S-1-5-21-10-20-30-1002" || d.flags != 0x202 {
		t.Fatalf("diagnostic=%+v error=%v", d, err)
	}
	want := []string{"role", "decode:role", "domain", "decode:domain", "user:" + b.name, "decode:user", "role", "decode:role", "domain", "decode:domain", "free:domain", "free:role", "free:user", "free:domain", "free:role"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("query/resource順序=%v", f.trace)
	}
	s, err := q.inspect(testContext(t), b)
	if err != ErrUnconfigured || s.complete || s.presence == absent || !s.disabled {
		t.Fatalf("診断を権限proofへ昇格=%+v %v", s, err)
	}
}
