package guestaccount

import (
	"testing"
)

func runtimeLayoutFixture(t *testing.T) []byte {
	t.Helper()
	data, _, _ := fixedLayoutFixture(t)
	return fixedLayoutMutate(t, data, func(v map[string]any) {
		v["protocol"] = "rescue-host-guest-runtime/1"
		v["logonProfile"] = "local-batch/1"
		p := v["policy"].(map[string]any)
		groups := p["groups"].([]any)
		for i, g := range groups {
			if g == "S-1-5-2" {
				groups[i] = "S-1-5-3"
			}
		}
		p["logonRights"] = []string{"SeBatchLogonRight", "SeDenyNetworkLogonRight", "SeDenyInteractiveLogonRight", "SeDenyRemoteInteractiveLogonRight", "SeDenyServiceLogonRight"}
	})
}

func TestRuntimeLayoutRequiresDistinctBatchProtocolAndCannotEnterNetworkDecoder(t *testing.T) {
	data := runtimeLayoutFixture(t)
	layout, err := DecodeRuntimeFixedLayout(data)
	if err != nil || layout == nil || !layout.runtime || !layout.policy.batch {
		t.Fatal("runtime Batch layout refused", err)
	}
	if _, err = DecodeFixedLayout(data); err == nil {
		t.Fatal("runtime became network staging")
	}
	network, _, _ := fixedLayoutFixture(t)
	if _, err = DecodeRuntimeFixedLayout(network); err == nil {
		t.Fatal("network became runtime")
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { delete(v, "logonProfile") },
		func(v map[string]any) { v["logonProfile"] = "network/1" },
		func(v map[string]any) { v["policy"].(map[string]any)["logonRights"] = []string{"SeNetworkLogonRight"} },
		func(v map[string]any) {
			v["policy"].(map[string]any)["logonRights"] = []string{"SeBatchLogonRight", "SeDenyBatchLogonRight"}
		},
	} {
		if _, err = DecodeRuntimeFixedLayout(fixedLayoutMutate(t, data, change)); err == nil {
			t.Fatal("ambiguous runtime layout accepted")
		}
	}
}
