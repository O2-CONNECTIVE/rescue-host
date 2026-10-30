package childjob

import (
	"context"
	"testing"
	"time"
)

func TestBatchGuestObservationAcceptsOnlyBatchProfile(t *testing.T) {
	p, inspection, _, api, ctx := guestTokenFixture(t)
	groups := []string{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545"}
	expectation, err := NewBatchGuestTokenExpectation(api.facts.user, groups, []string{"SeChangeNotifyPrivilege"})
	if err != nil {
		t.Fatal(err)
	}
	api.facts.groups[1].sid = "S-1-5-3"
	observation, err := p.ObserveGuestToken(ctx, inspection, expectation)
	if err != nil || !p.GuestTokenCurrent(inspection, observation) {
		t.Fatalf("batch observation was not retained: %v", err)
	}
}

func TestGuestProfilesRejectWrongAndMixedLogonGroups(t *testing.T) {
	const user = "S-1-5-21-7-8-9-2048"
	network := []string{"S-1-1-0", "S-1-5-2", "S-1-5-11", "S-1-5-32-545"}
	batch := []string{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545"}
	for _, tc := range []struct {
		name            string
		makeExpectation func(string, []string, []string) (GuestTokenExpectation, error)
		groups          []string
	}{
		{"network-cannot-declare-batch", NewGuestTokenExpectation, batch},
		{"batch-cannot-declare-network", NewBatchGuestTokenExpectation, network},
		{"network-cannot-declare-both", NewGuestTokenExpectation, append(append([]string{}, network...), "S-1-5-3")},
		{"batch-cannot-declare-both", NewBatchGuestTokenExpectation, append(append([]string{}, batch...), "S-1-5-2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.makeExpectation(user, tc.groups, []string{}); err == nil {
				t.Fatal("other or mixed logon profile accepted")
			}
		})
	}
}

func TestGuestProfilesRejectOtherProfileInObservedToken(t *testing.T) {
	for _, tc := range []struct {
		name     string
		batch    bool
		injected string
		replace  bool
	}{
		{"batch-observes-network", true, "S-1-5-2", false},
		{"network-observes-batch", false, "S-1-5-3", false},
		{"batch-replaced-by-network", true, "S-1-5-2", true},
		{"network-replaced-by-batch", false, "S-1-5-3", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, i, expectation, api, ctx := guestTokenFixture(t)
			if tc.batch {
				api.facts.groups[1].sid = "S-1-5-3"
				var err error
				expectation, err = NewBatchGuestTokenExpectation(api.facts.user, []string{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545"}, []string{"SeChangeNotifyPrivilege"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.replace {
				api.facts.groups[1].sid = tc.injected
			} else {
				api.facts.groups = append(api.facts.groups, guestTokenGroup{tc.injected, 7})
			}
			if observation, err := p.ObserveGuestToken(ctx, i, expectation); err == nil || observation != nil {
				t.Fatal("mixed observed token accepted")
			}
		})
	}
}

func TestBatchGuestRejectsUnsafeObservedFacts(t *testing.T) {
	for _, kind := range []string{"unknown-group", "admin-enabled", "admin-deny-only", "disabled-privilege", "missing-batch", "disabled-batch"} {
		t.Run(kind, func(t *testing.T) {
			p, i, _, api, ctx := guestTokenFixture(t)
			expectation, err := NewBatchGuestTokenExpectation(api.facts.user, []string{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545"}, []string{"SeChangeNotifyPrivilege"})
			if err != nil {
				t.Fatal(err)
			}
			api.facts.groups[1].sid = "S-1-5-3"
			switch kind {
			case "unknown-group":
				api.facts.groups = append(api.facts.groups, guestTokenGroup{"S-1-5-32-555", 7})
			case "admin-enabled":
				api.facts.groups = append(api.facts.groups, guestTokenGroup{"S-1-5-32-544", 7})
			case "admin-deny-only":
				api.facts.groups = append(api.facts.groups, guestTokenGroup{"S-1-5-32-544", 0x10})
			case "disabled-privilege":
				api.facts.privileges = append(api.facts.privileges, guestTokenPrivilege{"SeDebugPrivilege", 0, 44})
			case "missing-batch":
				api.facts.groups = append(api.facts.groups[:1], api.facts.groups[2:]...)
			case "disabled-batch":
				api.facts.groups[1].attributes = 0
			}
			if observation, err := p.ObserveGuestToken(ctx, i, expectation); err == nil || observation != nil {
				t.Fatal("unsafe observed batch token accepted")
			}
		})
	}
}

func TestBatchGuestObservationKeepsOriginalOwnershipAndLifetime(t *testing.T) {
	admission := &inspectionDeadlineContext{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
	p, i, _, api, _ := guestTokenFixture(t, admission)
	api.facts.groups[1].sid = "S-1-5-3"
	expectation, err := NewBatchGuestTokenExpectation(api.facts.user, []string{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545"}, []string{"SeChangeNotifyPrivilege"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, err := p.ObserveGuestToken(ctx, i, expectation)
	if err != nil {
		t.Fatal(err)
	}
	copy := *observation
	if p.GuestTokenCurrent(i, &copy) {
		t.Fatal("copied observation accepted")
	}
	admission.deadline = time.Now().Add(-time.Second)
	if p.GuestTokenCurrent(i, observation) || p.CommitInspected(ctx, i) == nil {
		t.Fatal("expired original admission was reused")
	}
}

func TestBatchGuestExpectationRejectsUnreviewedPolicyAndCopiesInput(t *testing.T) {
	const user = "S-1-5-21-7-8-9-2048"
	for _, groups := range [][]string{
		{"S-1-1-0", "S-1-5-11", "S-1-5-32-545"},
		{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545", "S-1-5-32-544"},
		{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545", "S-1-5-32-555"},
	} {
		if _, err := NewBatchGuestTokenExpectation(user, groups, []string{}); err == nil {
			t.Fatal("missing or privileged group accepted")
		}
	}
	groups := []string{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545"}
	if _, err := NewBatchGuestTokenExpectation(user, groups, []string{"SeDebugPrivilege"}); err == nil {
		t.Fatal("dangerous privilege accepted")
	}
	privileges := []string{"SeChangeNotifyPrivilege"}
	expectation, err := NewBatchGuestTokenExpectation(user, groups, privileges)
	if err != nil {
		t.Fatal(err)
	}
	groups[1], privileges[0] = "S-1-5-2", "SeDebugPrivilege"
	p, i, _, api, ctx := guestTokenFixture(t)
	api.facts.groups[1].sid = "S-1-5-3"
	if observation, err := p.ObserveGuestToken(ctx, i, expectation); err != nil || !p.GuestTokenCurrent(i, observation) {
		t.Fatal("caller mutation changed frozen expectation", err)
	}
}
