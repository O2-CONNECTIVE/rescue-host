package operatorauth

import (
	"strings"
	"testing"
)

func TestSIDProfileUsesExplicitModeWithoutLegacyAuthenticationWildcard(t *testing.T) {
	launch := strings.Repeat("a", 64)
	p := helperProfile{sid: fixedSIDPolicy(), launchID: launch}
	frame, err := encodeHelperProfileFrame(launch, p)
	if err != nil || strings.Contains(string(frame), "authentication") || strings.Contains(string(frame), "session") || !strings.Contains(string(frame), `"version":"2"`) {
		t.Fatal("explicit B profile missing", err)
	}
	got, err := decodeHelperProfileFrame(frame, launch)
	if err != nil || got != p {
		t.Fatal("policy changed in protected delivery", err)
	}
	if _, err = decodeHelperProfileFrame(frame, strings.Repeat("b", 64)); err == nil {
		t.Fatal("old launch accepted")
	}
	legacy := profilePipeTestProducer().(profilePipeProducerFixture).material.profile
	legacy.operator.authentication = 0
	if _, err = encodeHelperProfileFrame(launch, legacy); err == nil {
		t.Fatal("legacy zero authentication became wildcard")
	}
	for _, bad := range []string{
		strings.Replace(string(frame), `"version":"2"`, `"version":"1"`, 1),
		strings.Replace(string(frame), `"mode":"sid-message"`, `"mode":"unknown"`, 1),
		strings.Replace(string(frame), `"operatorSID":"S-1-5-21-1"`, `"operatorSID":"S-1-5-21-1","authentication":"17"`, 1),
		strings.Replace(string(frame), `"guestUser":`, `"guestUser":"S-1-5-21-3","guestUser":`, 1),
	} {
		if _, err = decodeHelperProfileFrame([]byte(bad), launch); err == nil {
			t.Fatal("ambiguous B profile accepted")
		}
	}
}
