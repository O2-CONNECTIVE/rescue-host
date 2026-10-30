package guestaccount

import (
	"context"
	"testing"
)

type runtimeInterruptedRightsFixture struct {
	runtimeRightsKernelFixture
	entered, release chan struct{}
	write            bool
	reads, removals  int
}

func (k *runtimeInterruptedRightsFixture) read(ctx context.Context, b binding, scope func() bool) ([]string, error) {
	if !scope() {
		return nil, ErrDenied
	}
	k.reads++
	return k.runtimeRightsKernelFixture.read(ctx, b, scope)
}
func (k *runtimeInterruptedRightsFixture) add(ctx context.Context, b binding, v []string, scope func() bool) error {
	if k.write {
		_ = k.runtimeRightsKernelFixture.add(ctx, b, v, scope)
	}
	close(k.entered)
	<-k.release
	return ErrUnknown // interrupted native return may or may not have applied the difference
}
func (k *runtimeInterruptedRightsFixture) remove(ctx context.Context, b binding, v []string, scope func() bool) error {
	k.removals++
	if !scope() {
		return ErrCleanup
	}
	for _, right := range v {
		if !k.rights[right] {
			return ErrDenied
		}
	}
	return k.runtimeRightsKernelFixture.remove(ctx, b, v, scope)
}
func TestRuntimeGuestRevokePreservesRightsCleanupScopeEvenWhenWriteNeverStarted(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-add", true: "after-add"}[write], func(t *testing.T) {
			r, _ := runtimeOwnerFixture(t)
			k := &runtimeInterruptedRightsFixture{runtimeRightsKernelFixture: runtimeRightsKernelFixture{rights: map[string]bool{"SeDenyNetworkLogonRight": true}}, entered: make(chan struct{}), release: make(chan struct{}), write: write}
			r.plan.policy.logonRights = []string{"SeBatchLogonRight", "SeDenyNetworkLogonRight"}
			held := newRuntimeRightsOwner(k, r.ops.scope)
			r.ops.rights = func() runtimeRights { return held }
			done := make(chan error, 1)
			go func() { _, err := r.Prepare(testContext(t)); done <- err }()
			defer releaseRuntimePhase(k.release)
			awaitRuntimePhase(t, k.entered)
			_ = r.Revoke()
			close(k.release)
			if err := awaitRuntimePhase(t, done); err == nil {
				t.Fatal("revoked rights acquisition accepted")
			}
			_ = r.Close(testContext(t)) // the uncertain account itself remains manual
			if !held.closed || k.rights["SeBatchLogonRight"] || !k.rights["SeDenyNetworkLogonRight"] {
				t.Fatal("Revoke disabled owned rights cleanup")
			}
			if !write && k.removals != 0 {
				t.Fatal("attempted removing rights already observed absent")
			}
		})
	}
}
