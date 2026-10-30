package guestaccount

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
)

func NewPlan(in ProtectedInputs) (Plan, error) {
	if administrativePolicy(in.policy) {
		return Plan{}, ErrDenied
	}
	if in.sessionEpoch == ([32]byte{}) || in.intentID == ([32]byte{}) || in.generation == 0 || !machineSID(in.machineSID) || !accountSID(in.operatorSID) || !serviceIdentity(in.serviceUserSID) || !in.policy.known || in.policy.groups == nil || in.policy.privileges == nil || in.policy.logonRights == nil || !labels(in.policy.groups, false) || !labels(in.policy.privileges, true) || !labels(in.policy.logonRights, true) {
		return Plan{}, ErrDenied
	}
	digest := sha256.Sum256(in.intentID[:])
	b := binding{epoch: in.sessionEpoch, intent: in.intentID, policy: policyDigest(in.policy), generation: in.generation, machine: in.machineSID, operator: in.operatorSID, service: in.serviceUserSID, name: "rhg_" + hex.EncodeToString(digest[:8])}
	return Plan{binding: b, policy: copyPolicy(in.policy), valid: true}, nil
}
func policyDigest(p limitedPolicy) [32]byte {
	h := sha256.New()
	if p.batch {
		h.Write([]byte("rescue-host-local-batch/1\x00"))
	}
	for _, list := range [][]string{p.groups, p.privileges, p.logonRights} {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(list))))
		for _, v := range list {
			h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(v))))
			h.Write([]byte(v))
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func machineSID(s string) bool { return sidParts(s, 3) }
func accountSID(s string) bool { return sidParts(s, 4) }
func serviceIdentity(s string) bool {
	if accountSID(s) || s == "S-1-5-18" || s == "S-1-5-19" || s == "S-1-5-20" {
		return true
	}
	if !strings.HasPrefix(s, "S-1-5-80-") {
		return false
	}
	return sidParts("S-1-5-21-"+strings.TrimPrefix(s, "S-1-5-80-"), 5)
}
func sidParts(s string, count int) bool {
	if !strings.HasPrefix(s, "S-1-5-21-") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(s, "S-1-5-21-"), "-")
	if len(parts) != count {
		return false
	}
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}
func labels(values []string, empty bool) bool {
	if (!empty && len(values) == 0) || len(values) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if len(v) == 0 || len(v) > 184 || seen[v] || strings.ContainsAny(v, "\x00\r\n") {
			return false
		}
		seen[v] = true
	}
	return true
}
func copyPolicy(p limitedPolicy) limitedPolicy {
	p.groups = append([]string{}, p.groups...)
	p.privileges = append([]string{}, p.privileges...)
	p.logonRights = append([]string{}, p.logonRights...)
	return p
}
func (p Plan) validBinding(b binding) bool {
	want := p.binding
	want.sid = b.sid
	return p.valid && b == want && accountSID(b.sid) && strings.HasPrefix(b.sid, b.machine+"-") && b.sid != b.operator && b.sid != b.service
}
