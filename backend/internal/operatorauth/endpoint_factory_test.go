package operatorauth

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"testing"
)

// Constructor provenance is not authenticated Read evidence. The existing
// private batch/kernel fixture models only prepared owner/source identity.
func TestEndpointFactoryAndBatchRequireExactFactoryAndSoleInput(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	s.input = 0
	policy, _ := providerFixture()
	service := new(bootstrapauth.Factory)
	factory, err := newEndpointFactory(s, service, 71, policy)
	if err != nil || !factory.MatchesServiceFactory(service) || factory.MatchesServiceFactory(new(bootstrapauth.Factory)) {
		t.Fatal("factory provenance not immutable", err)
	}
	copy := *factory
	if copy.MatchesServiceFactory(service) {
		t.Fatal("copied factory accepted")
	}
	input := new(serviceready.SoleInput)
	if batch, err := copy.Prepare(input, owner.owner, owner.issuer); batch != nil || err == nil {
		t.Fatal("copy can prepare")
	}
	if batch, err := (*EndpointFactory)(nil).Prepare(input, owner.owner, owner.issuer); batch != nil || err == nil {
		t.Fatal("nil factory can prepare")
	}
	s.soleInput = input
	s.inputLease = &endpointCandidateLease{api: endpointCandidateFixture()}
	ops.moveSoleInput = moveNativeSoleInput
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	b.serviceFactory = service // private native-constructor seam only
	if !b.MatchesServiceFactory(service) || !b.MatchesSoleInput(input) || b.MatchesSoleInput(new(serviceready.SoleInput)) || b.MatchesServiceFactory(new(bootstrapauth.Factory)) {
		t.Fatal("batch did not retain exact immutable source/factory")
	}
}
