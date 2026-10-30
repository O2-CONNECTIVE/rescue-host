package guestaccount

import (
	"context"
	"testing"
)

type runtimeRightsKernelFixture struct {
	rights         map[string]bool
	added, removed []string
	partial        bool
}

func (k *runtimeRightsKernelFixture) read(context.Context, binding, func() bool) ([]string, error) {
	out := []string{}
	for r := range k.rights {
		out = append(out, r)
	}
	return out, nil
}
func (k *runtimeRightsKernelFixture) add(_ context.Context, _ binding, rights []string, _ func() bool) error {
	for _, r := range rights {
		k.added = append(k.added, r)
		k.rights[r] = true
	}
	if k.partial {
		return ErrUnknown
	}
	return nil
}
func (k *runtimeRightsKernelFixture) remove(_ context.Context, _ binding, rights []string, _ func() bool) error {
	for _, r := range rights {
		k.removed = append(k.removed, r)
		delete(k.rights, r)
	}
	return nil
}
func (k *runtimeRightsKernelFixture) close(context.Context, func() bool) error { return nil }
func TestRuntimeGuestRetainsPartialRightsAttemptAndRemovesOnlyItsOwnDifference(t *testing.T) {
	r, _ := runtimeOwnerFixture(t)
	k := &runtimeRightsKernelFixture{rights: map[string]bool{"SeDenyNetworkLogonRight": true}, partial: true}
	r.plan.policy.logonRights = []string{"SeBatchLogonRight", "SeDenyNetworkLogonRight"}
	r.ops.rights = func() runtimeRights { return newRuntimeRightsOwner(k) }
	if _, err := r.Prepare(testContext(t)); err == nil {
		t.Fatal("partial rights succeeded")
	}
	if len(k.added) != 1 || k.added[0] != "SeBatchLogonRight" {
		t.Fatal("preexisting right rewritten")
	}
	_ = r.Close(testContext(t)) // the uncertain account outcome remains manual
	if len(k.removed) != 1 || k.removed[0] != "SeBatchLogonRight" || !k.rights["SeDenyNetworkLogonRight"] || k.rights["SeBatchLogonRight"] {
		t.Fatal("rights cleanup changed preexisting state")
	}
}
