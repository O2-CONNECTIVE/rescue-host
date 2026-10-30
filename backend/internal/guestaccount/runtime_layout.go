package guestaccount

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"reflect"
	"unicode/utf8"
)

// DecodeRuntimeFixedLayout accepts only the separate local Batch candidate.
// These nonsecret expectations are neither OS configuration nor approval.
func DecodeRuntimeFixedLayout(data []byte) (*FixedLayout, error) {
	if len(data) == 0 || len(data) > fixedLayoutBytes || !utf8.Valid(data) || !fixedLayoutUnicode(data) {
		return nil, ErrUnconfigured
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := fixedLayoutValue(d, 0)
	if err != nil {
		return nil, ErrUnconfigured
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrUnconfigured
	}
	object, ok := v.(map[string]any)
	if !ok || object["logonProfile"] != "local-batch/1" {
		return nil, ErrUnconfigured
	}
	delete(object, "logonProfile")
	if !fixedLayoutShape(object, reflect.TypeOf(fixedLayoutWire{})) {
		return nil, ErrUnconfigured
	}
	var w fixedLayoutWire
	if json.Unmarshal(data, &w) != nil || w.Protocol != "rescue-host-guest-runtime/1" {
		return nil, ErrUnconfigured
	}
	f, err := fixedLayoutConvert(w)
	if err != nil {
		return nil, err
	}
	f.policy.batch = true
	if f.user != "S-1-5-18" || !validFixedLayoutFiles(f) || !runtimeLayoutPolicyAllowed(f.machine, f.policy) {
		return nil, ErrUnconfigured
	}
	f.runtime, f.configured, f.digest = true, true, sha256.Sum256(data)
	return f, nil
}

func runtimeLayoutPolicyAllowed(machine string, p limitedPolicy) bool {
	required := []string{machine + "-513", "S-1-1-0", "S-1-2-0", "S-1-5-11", "S-1-5-113", "S-1-5-15", "S-1-5-3", "S-1-5-32-545"}
	if !p.known || !p.batch || administrativePolicy(p) || !labels(p.groups, false) || len(p.groups) != len(required) || !labels(p.privileges, true) || !labels(p.logonRights, false) {
		return false
	}
	for _, g := range p.groups {
		if !containsPermissionSID(required, g) {
			return false
		}
	}
	for _, r := range p.privileges {
		if r != "SeChangeNotifyPrivilege" && r != "SeIncreaseWorkingSetPrivilege" && r != "SeTimeZonePrivilege" {
			return false
		}
	}
	for _, r := range p.logonRights {
		switch r {
		case "SeBatchLogonRight", "SeDenyNetworkLogonRight", "SeDenyInteractiveLogonRight", "SeDenyRemoteInteractiveLogonRight", "SeDenyServiceLogonRight":
		default:
			return false
		}
	}
	return containsPermissionSID(p.logonRights, "SeBatchLogonRight")
}
