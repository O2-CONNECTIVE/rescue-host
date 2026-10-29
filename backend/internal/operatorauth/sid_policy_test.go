package operatorauth

import (
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"testing"
)

func fixedSIDPolicy() SIDPolicy { return SIDPolicy{OperatorSID: "S-1-5-21-1", GuestSID: "S-1-5-21-2"} }

func TestSIDFactoryRetainsPolicyWithoutAcquiringHelperResources(t *testing.T) {
	service := new(bootstrapauth.Factory)
	p := fixedSIDPolicy()
	f, err := NewSIDEndpointFactory(service, p.OperatorSID, p)
	if err != nil || !f.MatchesServiceFactory(service) || !f.MatchesOperatorSID(p.OperatorSID) || !f.MatchesSIDPolicy(p.OperatorSID, p.GuestSID) || f.RuntimeConfigured() {
		t.Fatal("static policy unavailable", err)
	}
	_, owner, _, _, _, _ := batchFixture(t)
	if b, err := f.Prepare(new(serviceready.SoleInput), owner.owner, owner.issuer); b != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("missing helper runtime acquired resources", err)
	}
	copy := *f
	if copy.MatchesOperatorSID(p.OperatorSID) {
		t.Fatal("copy accepted")
	}
	p.OperatorSID = "S-1-5-21-3"
	if !f.MatchesOperatorSID("S-1-5-21-1") {
		t.Fatal("caller mutated policy")
	}
	if got, err := NewSIDEndpointFactory(service, "S-1-5-21-3", fixedSIDPolicy()); got != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("mismatched protected operator accepted")
	}
}
