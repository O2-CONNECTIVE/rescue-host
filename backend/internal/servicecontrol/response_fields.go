package servicecontrol

import "strconv"

func unsignedDecimal(value any) (uint64, bool) {
	s, ok := value.(string)
	if !ok || len(s) == 0 || len(s) > 20 {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil && strconv.FormatUint(n, 10) == s
}
func nullableHex32(value any) (*string, bool) {
	if value == nil {
		return nil, true
	}
	s, ok := lowerHex(value, 32)
	if !ok {
		return nil, false
	}
	return &s, true
}
func oneOf(s string, options ...string) bool {
	for _, v := range options {
		if v == s {
			return true
		}
	}
	return false
}
func bindingShape(value any) (*UntrustedBinding, bool) {
	if value == nil {
		return nil, true
	}
	object, ok := value.(map[string]any)
	if !ok || !exactFields(object, "generation", "identity") {
		return nil, false
	}
	generation, okGeneration := positiveDecimal(object["generation"])
	identity, ok := object["identity"].(map[string]any)
	if !ok || !okGeneration || !exactFields(identity, "launchId", "epoch", "policyDigest") {
		return nil, false
	}
	launch, okLaunch := identity["launchId"].(string)
	epoch, okEpoch := unsignedDecimal(identity["epoch"])
	digest, okDigest := lowerHex(identity["policyDigest"], 64)
	if !okLaunch || !launchID(launch) || !okEpoch || !okDigest {
		return nil, false
	}
	return &UntrustedBinding{Generation: generation, Identity: UntrustedIdentity{LaunchID: launch, Epoch: epoch, PolicyDigest: digest}}, true
}
func launchID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func boundedResponse(r UntrustedResponse) bool {
	if len(r.RequestID) > 32 || len(r.ClientNonce) > 64 || len(r.BootEpoch) > 64 || len(r.ChannelNonce) > 64 || len(r.Command) > 32 || len(r.Result) > 32 {
		return false
	}
	count := 0
	for _, present := range []bool{r.Snapshot != nil, r.Intent != nil, r.Approval != nil, r.Quiesce != nil, r.Error != nil} {
		if present {
			count++
		}
	}
	if r.Type == "hello" {
		return count == 0 && r.Command == "" && r.Result == "" && r.Sequence == 0
	}
	if r.Type != "response" || r.ClientNonce != "" || r.RequestLimit != 0 || count != 1 {
		return false
	}
	switch {
	case r.Snapshot != nil:
		if r.Command != Status || r.Result != "snapshot" {
			return false
		}
		p := r.Snapshot
		if len(p.ServicePhase) > 16 || len(p.LaunchPhase) > 32 || p.ApprovalRequestID != nil && len(*p.ApprovalRequestID) > 32 {
			return false
		}
		if p.Binding != nil && (len(p.Binding.Identity.LaunchID) > 128 || len(p.Binding.Identity.PolicyDigest) > 64) {
			return false
		}
	case r.Intent != nil:
		if r.Command != PrepareIntent || r.Result != "intent-prepared" || len(r.Intent.IntentID) > 32 || len(r.Intent.PreviewDigest) > 64 || !validPreview(r.Intent.Preview) {
			return false
		}
	case r.Approval != nil:
		if r.Command != ConfirmApproval || r.Result != "approval-accepted" || len(r.Approval.ApprovalRequestID) > 32 {
			return false
		}
	case r.Quiesce != nil:
		if r.Command != RevokeAllAndQuiesce || !oneOf(r.Result, "quiesce-accepted", "workload-quiesced") {
			return false
		}
	case r.Error != nil:
		if r.Result != "error" || len(r.Error.Code) > 64 {
			return false
		}
	}
	return true
}
