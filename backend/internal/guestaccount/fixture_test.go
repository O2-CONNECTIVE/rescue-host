package guestaccount

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fixtureはOS/SAM/journal境界の合成producer。実アカウントに接続しない。
type fixture struct {
	trace                                   []string
	s                                       sources
	stored                                  record
	reserved                                bool
	snapshot                                accountSnapshot
	fail                                    map[string]error
	panics                                  map[string]bool
	corrupt                                 map[string]bool
	partial, omitCreate, deleteDespiteError bool
	createEntered, createRelease            chan struct{}
	quiesceEntered, quiesceRelease          chan struct{}
	journalHook                             func(stage)
	createHook                              func()
	inspectHook                             func(*accountSnapshot)
	oldBinding                              binding
}

func testPlan(t *testing.T) Plan {
	t.Helper()
	p, err := NewPlan(ProtectedInputs{sessionEpoch: [32]byte{1}, intentID: [32]byte{2}, generation: 1, machineSID: "S-1-5-21-10-20-30", operatorSID: "S-1-5-21-10-20-30-1001", serviceUserSID: "S-1-5-18", policy: limitedPolicy{known: true, groups: []string{"synthetic-users"}, privileges: []string{}, logonRights: []string{"synthetic-ssh"}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func newFixture(t *testing.T) (*Lifecycle, *fixture) {
	t.Helper()
	f := &fixture{fail: map[string]error{}, panics: map[string]bool{}, corrupt: map[string]bool{}}
	f.s = sources{journal: f, accounts: f, session: f, journalSeal: &seal{1}, accountSeal: &seal{2}, sessionSeal: &seal{3}}
	l, err := newLifecycle(testPlan(t), f.s)
	if err != nil {
		t.Fatal(err)
	}
	return l, f
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
func (f *fixture) step(name string) error {
	f.trace = append(f.trace, name)
	if f.panics[name] {
		panic("合成producer panic")
	}
	return f.fail[name]
}
func (f *fixture) journalResult(r record, op string) (journalReceipt, error) {
	err := f.step(op)
	if err != nil {
		return journalReceipt{}, err
	}
	f.stored = r
	if f.journalHook != nil {
		f.journalHook(r.stage)
	}
	if f.corrupt[op] {
		r.binding.generation++
	}
	return journalReceipt{f.s.journalSeal, r}, nil
}
func (f *fixture) reserve(_ context.Context, r record) (journalReceipt, error) {
	if f.reserved {
		return journalReceipt{}, knownFailure{}
	}
	f.reserved = true
	return f.journalResult(r, "reserve")
}
func (f *fixture) save(_ context.Context, r record) (journalReceipt, error) {
	return f.journalResult(r, fmt.Sprintf("commit:%d", r.stage))
}
func (f *fixture) load(context.Context) (journalReceipt, error) {
	err := f.step("load")
	r := f.stored
	if f.corrupt["load"] {
		r.binding.epoch[0]++
	}
	return journalReceipt{f.s.journalSeal, r}, err
}
func (f *fixture) createDisabled(_ context.Context, b binding) (accountReceipt, error) {
	err := f.step("create")
	if f.createEntered != nil {
		close(f.createEntered)
		<-f.createRelease
	}
	if f.createHook != nil {
		f.createHook()
	}
	b.sid = b.machine + "-1002"
	f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: b, presence: exact, complete: true, disabled: true, groups: []string{"synthetic-users"}, privileges: []string{}, logonRights: []string{"synthetic-ssh"}}
	if f.corrupt["create"] {
		b.intent[0]++
	}
	if f.omitCreate || (err != nil && !f.partial) {
		return accountReceipt{}, err
	}
	return accountReceipt{f.s.accountSeal, b, created}, err
}
func (f *fixture) inspect(_ context.Context, b binding) (accountSnapshot, error) {
	err := f.step("inspect")
	r := f.snapshot
	if f.inspectHook != nil {
		f.inspectHook(&r)
	}
	if f.corrupt["inspect"] {
		r.binding.generation++
	}
	return r, err
}
func (f *fixture) disable(_ context.Context, b binding) (accountReceipt, error) {
	err := f.step("disable")
	if err != nil {
		return accountReceipt{}, err
	}
	if f.snapshot.binding != b || f.snapshot.presence != exact {
		return accountReceipt{}, knownFailure{}
	}
	f.snapshot.disabled = true
	if f.corrupt["disable"] {
		b.epoch[0]++
	}
	return accountReceipt{f.s.accountSeal, b, disabled}, nil
}
func (f *fixture) deleteExact(_ context.Context, b binding) (accountReceipt, error) {
	err := f.step("delete")
	if err == nil || f.deleteDespiteError {
		f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: b, presence: absent, complete: true}
	}
	if err != nil {
		return accountReceipt{}, err
	}
	if f.corrupt["delete"] {
		b.sid = b.machine + "-9999"
	}
	return accountReceipt{f.s.accountSeal, b, deleted}, nil
}
func (f *fixture) revoke(_ context.Context, b binding) (sessionReceipt, error) {
	err := f.step("revoke")
	if f.corrupt["revoke"] {
		b.epoch[0]++
	}
	return sessionReceipt{f.s.sessionSeal, b, revoked}, err
}
func (f *fixture) quiesce(_ context.Context, b binding) (sessionReceipt, error) {
	err := f.step("quiesce")
	if f.quiesceEntered != nil {
		close(f.quiesceEntered)
		<-f.quiesceRelease
	}
	if f.corrupt["quiesce"] {
		b.intent[0]++
	}
	return sessionReceipt{f.s.sessionSeal, b, quiescent}, err
}
func (f *fixture) previousBinding() binding { return f.oldBinding }
func (f *fixture) revokePrevious(_ context.Context, b binding) (sessionReceipt, error) {
	err := f.step("revoke-old")
	if f.corrupt["revoke-old"] {
		b.epoch[0]++
	}
	return sessionReceipt{f.s.sessionSeal, b, revoked}, err
}
func (f *fixture) quiescePrevious(_ context.Context, b binding) (sessionReceipt, error) {
	err := f.step("quiesce-old")
	if f.corrupt["quiesce-old"] {
		b.intent[0]++
	}
	return sessionReceipt{f.s.sessionSeal, b, quiescent}, err
}
