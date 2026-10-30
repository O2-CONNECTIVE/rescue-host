package guestaccount

import (
	"context"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

func awaitRuntimePhase[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-testContext(t).Done():
		t.Fatal("runtime fixture phase did not complete within its budget")
	}
	var zero T
	return zero
}
func releaseRuntimePhase(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

type runtimePhaseTokenFixture struct {
	runtimeTokenFixture
	entered, release chan struct{}
	duplicated       bool
}

func (t *runtimePhaseTokenFixture) acquire(_ context.Context, _ binding, _ []uint16, admit func() bool) error {
	if !admit() {
		return ErrDenied
	}
	t.f.trace = append(t.f.trace, "logon")
	close(t.entered)
	<-t.release // the admitted native logon actually returns late
	if admit() {
		t.duplicated = true
	}
	return nil
}

func TestRuntimeGuestRevokeBlocksNewLogonAfterOwnerAllocation(t *testing.T) {
	r, f := runtimeOwnerFixture(t)
	allocated, release := make(chan struct{}), make(chan struct{})
	token := &runtimePhaseTokenFixture{runtimeTokenFixture: runtimeTokenFixture{f: f}, entered: make(chan struct{}), release: make(chan struct{})}
	close(token.release) // an unexpected admitted call must fail the assertion, not hang
	r.ops.token = func() runtimeToken { close(allocated); <-release; return token }
	done := make(chan error, 1)
	go func() { _, err := r.Prepare(testContext(t)); done <- err }()
	defer releaseRuntimePhase(release)
	awaitRuntimePhase(t, allocated)
	_ = r.Revoke()
	close(release)
	if err := awaitRuntimePhase(t, done); err == nil {
		t.Fatal("revoked new logon accepted")
	}
	for _, phase := range f.trace {
		if phase == "logon" {
			t.Fatal("new native logon admitted after Revoke")
		}
	}
	if err := r.Close(testContext(t)); err != nil || !token.closed {
		t.Fatal("Revoke disabled allocated token owner cleanup", err)
	}
}
func TestRuntimeGuestRevokeBlocksDuplicateAfterLateLogonWithoutCancelingCleanup(t *testing.T) {
	r, f := runtimeOwnerFixture(t)
	token := &runtimePhaseTokenFixture{runtimeTokenFixture: runtimeTokenFixture{f: f}, entered: make(chan struct{}), release: make(chan struct{})}
	r.ops.token = func() runtimeToken { return token }
	done := make(chan error, 1)
	go func() { _, err := r.Prepare(testContext(t)); done <- err }()
	defer releaseRuntimePhase(token.release)
	awaitRuntimePhase(t, token.entered)
	if err := r.Revoke(); err != nil {
		t.Fatal(err)
	}
	close(token.release)
	if err := awaitRuntimePhase(t, done); err == nil {
		t.Fatal("revoked logon accepted")
	}
	if token.duplicated {
		t.Fatal("native duplication admitted after Revoke")
	}
	if err := r.Close(testContext(t)); err != nil || !token.closed {
		t.Fatal("Revoke disabled original token cleanup", err)
	}
}

type runtimePhaseTargetFixture struct {
	entered, release chan struct{}
	created, closed  bool
	cleanupScope     func() bool
}

func (t *runtimePhaseTargetFixture) prepare(_ context.Context, _ binding, _ uintptr, _ childjob.Spec, admit func() bool) (*childjob.Prepared, error) {
	close(t.entered)
	<-t.release // the owned pipe acquisition has actually returned
	if !admit() {
		return nil, ErrDenied
	}
	t.created = true
	return &childjob.Prepared{}, nil
}
func (t *runtimePhaseTargetFixture) Close(context.Context) error {
	if !t.cleanupScope() {
		return ErrCleanup
	}
	t.closed = true
	return nil
}
func TestRuntimeGuestRevokeBlocksChildPreparationAfterPipeReturnAndRetainsCleanup(t *testing.T) {
	r, f := runtimeOwnerFixture(t)
	target := &runtimePhaseTargetFixture{entered: make(chan struct{}), release: make(chan struct{}), cleanupScope: r.ops.scope}
	r.ops.target = func() runtimeTarget { return target }
	done := make(chan error, 1)
	go func() { _, err := r.Prepare(testContext(t)); done <- err }()
	defer releaseRuntimePhase(target.release)
	awaitRuntimePhase(t, target.entered)
	if err := r.Revoke(); err != nil {
		t.Fatal(err)
	}
	close(target.release)
	if err := awaitRuntimePhase(t, done); err == nil {
		t.Fatal("revoked target accepted")
	}
	if target.created {
		t.Fatal("native child creation admitted after Revoke")
	}
	if err := r.Close(testContext(t)); err != nil || !target.closed || !f.token.closed {
		t.Fatal("Revoke disabled original resource cleanup", err)
	}
}
