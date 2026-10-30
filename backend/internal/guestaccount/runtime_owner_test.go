package guestaccount

import (
	"context"
	"reflect"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

type runtimeFixture struct {
	trace  []string
	secret []uint16
	token  *runtimeTokenFixture
}

type runtimeAuthorityFixture struct{ f *runtimeFixture }

func (a runtimeAuthorityFixture) bind(context.Context, binding) error {
	a.f.trace = append(a.f.trace, "bind")
	return nil
}
func (a runtimeAuthorityFixture) revoke(context.Context, binding) error {
	a.f.trace = append(a.f.trace, "revoke")
	return nil
}

type runtimeRightsFixture struct{ f *runtimeFixture }

func (a *runtimeRightsFixture) provision(context.Context, binding, []string, func() bool) error {
	a.f.trace = append(a.f.trace, "rights")
	return nil
}
func (a *runtimeRightsFixture) Close(context.Context) error {
	a.f.trace = append(a.f.trace, "rights-close")
	return nil
}

type runtimeCredentialFixture struct {
	f            *runtimeFixture
	prepareError error
}

func (a *runtimeCredentialFixture) prepare(context.Context, func() bool) error { return a.prepareError }
func (a *runtimeCredentialFixture) provision(_ context.Context, _ binding, s []uint16, _ func() bool) error {
	a.f.trace = append(a.f.trace, "credential")
	a.f.secret = s
	return nil
}
func (a *runtimeCredentialFixture) Close(context.Context) error {
	a.f.trace = append(a.f.trace, "credential-close")
	return nil
}

type runtimeTokenFixture struct {
	f                *runtimeFixture
	closed           bool
	entered, release chan struct{}
}

func (a *runtimeTokenFixture) acquire(context.Context, binding, []uint16, func() bool) error {
	a.f.trace = append(a.f.trace, "logon")
	if a.entered != nil {
		close(a.entered)
		<-a.release
	}
	return nil
}
func (a *runtimeTokenFixture) value() uintptr { return 20 }
func (a *runtimeTokenFixture) inspect(context.Context, binding, func() bool) error {
	a.f.trace = append(a.f.trace, "token-inspect")
	return nil
}
func (a *runtimeTokenFixture) Close(context.Context) error {
	a.f.trace = append(a.f.trace, "token-close")
	a.closed = true
	return nil
}

type runtimeTargetFixture struct {
	f        *runtimeFixture
	prepared *childjob.Prepared
}

func (a *runtimeTargetFixture) prepare(context.Context, binding, uintptr, childjob.Spec, func() bool) (*childjob.Prepared, error) {
	a.f.trace = append(a.f.trace, "prepare-target")
	return a.prepared, nil
}
func (a *runtimeTargetFixture) Close(context.Context) error {
	a.f.trace = append(a.f.trace, "target-close")
	return nil
}

func runtimeOwnerFixture(t *testing.T) (*RuntimeGuest, *runtimeFixture) {
	t.Helper()
	l, f := newFixture(t)
	l.plan.policy.batch = true
	rf := &runtimeFixture{}
	rf.token = &runtimeTokenFixture{f: rf}
	ops := runtimeOps{scope: func() bool { return true }, random: func(b []byte) error {
		for i := range b {
			b[i] = byte(i)
		}
		return nil
	}, rights: func() runtimeRights { return &runtimeRightsFixture{rf} }, credential: func() runtimeCredential { return &runtimeCredentialFixture{f: rf} }, token: func() runtimeToken { return rf.token }, target: func() runtimeTarget { return &runtimeTargetFixture{rf, &childjob.Prepared{}} }, check: func(context.Context, binding) error { rf.trace = append(rf.trace, "check"); return nil }}
	r, err := newRuntimeGuest(l.plan, runtimeAuthorityFixture{rf}, ops)
	if err != nil {
		t.Fatal(err)
	}
	r.stage, r.lifecycle = l, l
	l.sources.session = r
	r.sessionSeal = f.s.sessionSeal
	return r, rf
}
func TestRuntimeGuestCreatesOwnedTokenAndTargetWithoutExposingCredential(t *testing.T) {
	r, f := runtimeOwnerFixture(t)
	p, err := r.Prepare(testContext(t))
	if err != nil || p == nil {
		t.Fatal("runtime target refused", err)
	}
	want := []string{"bind", "rights", "check", "credential", "logon", "token-inspect", "credential-close", "prepare-target"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatal("runtime lifecycle order", f.trace)
	}
	for _, c := range f.secret {
		if c != 0 {
			t.Fatal("credential remained after actual logon/helper return")
		}
	}
	if f.token.closed {
		t.Fatal("borrowed primary token closed before target")
	}
	if _, err = r.Prepare(testContext(t)); err == nil {
		t.Fatal("second runtime attempt")
	}
	if err = r.Close(testContext(t)); err != nil {
		t.Fatal("runtime close", err)
	}
	if !f.token.closed {
		t.Fatal("runtime retained token after target closure")
	}
}
