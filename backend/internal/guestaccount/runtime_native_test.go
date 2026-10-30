package guestaccount

import (
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func TestNativeRuntimeGuestCannotAdoptLegacyNetworkLayout(t *testing.T) {
	data, _, _ := fixedLayoutFixture(t)
	legacy, err := DecodeFixedLayout(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []*FixedLayout{nil, legacy} {
		owner, err := NewRuntimeGuest(testContext(t), layout, nil, serviceready.Config{}, nil, servicehost.RuntimeGuestTicket{}, nil, nil)
		if err == nil || owner != nil {
			t.Fatal("legacy or absent layout acquired a runtime owner")
		}
	}
}
