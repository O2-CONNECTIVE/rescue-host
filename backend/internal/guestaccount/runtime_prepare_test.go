package guestaccount

import (
	"context"
	"testing"
)

type ownedPreparationFixture struct {
	*fixture
	lifecycle *Lifecycle
	called    bool
}

func (s *ownedPreparationFixture) prepareOwnedAccount(_ context.Context, b binding) error {
	if s.lifecycle.record.stage != ownedSID || s.lifecycle.record.binding != b {
		return ErrDenied
	}
	s.called = true
	s.trace = append(s.trace, "runtime-rights")
	return nil
}
func TestRuntimeRightsPreparationRequiresDurableOwnedSIDBeforeLimitedInspection(t *testing.T) {
	l, f := newFixture(t)
	s := &ownedPreparationFixture{fixture: f, lifecycle: l}
	l.sources.session = s
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if !s.called {
		t.Fatal("runtime rights were not prepared")
	}
	for i, v := range f.trace {
		if v == "runtime-rights" && (i == 0 || f.trace[i-1] != "commit:3" || i+1 == len(f.trace) || f.trace[i+1] != "inspect") {
			t.Fatal("runtime account preparation order")
		}
	}
}
