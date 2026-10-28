package servicecontrol

import (
	"encoding/json"
	"strconv"
)

// UntrustedResponseは未認証のwire値であり、承認・停止・実行の証明ではない。
type UntrustedResponse struct {
	Type                                            string
	Sequence                                        uint64
	RequestID, ClientNonce, BootEpoch, ChannelNonce string
	RequestLimit                                    uint64
	Command                                         Command
	Result                                          string
	Snapshot                                        *UntrustedSnapshot
	Intent                                          *UntrustedIntent
	Approval                                        *UntrustedApproval
	Quiesce                                         *UntrustedQuiesce
	Error                                           *UntrustedServiceError
}

func DecodeUntrustedResponseFrame(frame []byte) (UntrustedResponse, error) {
	object, err := decodeFrame(frame)
	if err != nil {
		return UntrustedResponse{}, err
	}
	return responseShape(object)
}

func responseShape(object map[string]any) (UntrustedResponse, error) {
	if object["protocol"] != Protocol {
		return UntrustedResponse{}, ErrSchema
	}
	id, a := lowerHex(object["requestId"], 32)
	boot, b := lowerHex(object["bootEpoch"], 64)
	channel, c := lowerHex(object["channelNonce"], 64)
	if !a || !b || !c {
		return UntrustedResponse{}, ErrSchema
	}
	out := UntrustedResponse{RequestID: id, BootEpoch: boot, ChannelNonce: channel}
	if object["type"] == "hello" {
		client, ok := lowerHex(object["clientNonce"], 64)
		limit, okLimit := positiveDecimal(object["requestLimit"])
		if !ok || !okLimit || limit > 1024 || object["sequence"] != "0" || !exactFields(object, "protocol", "type", "sequence", "requestId", "clientNonce", "bootEpoch", "channelNonce", "requestLimit") {
			return UntrustedResponse{}, ErrSchema
		}
		out.Type = "hello"
		out.ClientNonce = client
		out.RequestLimit = limit
		return out, nil
	}
	if object["type"] != "response" || !exactFields(object, "protocol", "type", "bootEpoch", "channelNonce", "sequence", "requestId", "command", "result", "body") {
		return UntrustedResponse{}, ErrSchema
	}
	sequence, okSequence := positiveDecimal(object["sequence"])
	body, okBody := object["body"].(map[string]any)
	command, okCommand := object["command"].(string)
	result, okResult := object["result"].(string)
	if !okSequence || !okBody || !okCommand || !okResult {
		return UntrustedResponse{}, ErrSchema
	}
	out.Type = "response"
	out.Sequence = sequence
	out.Command = Command(command)
	out.Result = result
	switch {
	case out.Command == Status && result == "snapshot":
		snapshot, err := snapshotShape(body)
		if err != nil {
			return UntrustedResponse{}, err
		}
		out.Snapshot = &snapshot
	case out.Command == PrepareIntent && result == "intent-prepared":
		intent, err := intentShape(body, boot)
		if err != nil {
			return UntrustedResponse{}, err
		}
		out.Intent = &intent
	case out.Command == ConfirmApproval && result == "approval-accepted":
		approval, ok := lowerHex(body["approvalRequestId"], 32)
		if !exactFields(body, "approvalRequestId") || !ok || approval != id {
			return UntrustedResponse{}, ErrSchema
		}
		out.Approval = &UntrustedApproval{ApprovalRequestID: approval}
	case out.Command == RevokeAllAndQuiesce && oneOf(result, "quiesce-accepted", "workload-quiesced"):
		revoked, okRevoked := body["revocationApplied"].(bool)
		cleanup, okCleanup := body["workloadCleanupConfirmed"].(bool)
		if !exactFields(body, "revocationApplied", "workloadCleanupConfirmed") || !okRevoked || !okCleanup || !revoked || cleanup != (result == "workload-quiesced") {
			return UntrustedResponse{}, ErrSchema
		}
		out.Quiesce = &UntrustedQuiesce{RevocationApplied: revoked, WorkloadCleanupConfirmed: cleanup}
	case oneOf(command, string(Status), string(PrepareIntent), string(ConfirmApproval), string(RevokeAllAndQuiesce)) && result == "error":
		code, ok := body["code"].(string)
		if !ok || !exactFields(body, "code") || !serviceErrorCode(code) {
			return UntrustedResponse{}, ErrSchema
		}
		out.Error = &UntrustedServiceError{Code: code}
	default:
		return UntrustedResponse{}, ErrSchema
	}
	return out, nil
}

func snapshotShape(body map[string]any) (UntrustedSnapshot, error) {
	if !exactFields(body, "servicePhase", "generation", "approvalRequestId", "launchPhase", "binding", "workloadCleanupConfirmed") {
		return UntrustedSnapshot{}, ErrSchema
	}
	phase, okPhase := body["servicePhase"].(string)
	launch, okLaunch := body["launchPhase"].(string)
	generation, okGeneration := unsignedDecimal(body["generation"])
	cleanup, okCleanup := body["workloadCleanupConfirmed"].(bool)
	approval, okApproval := nullableHex32(body["approvalRequestId"])
	binding, okBinding := bindingShape(body["binding"])
	if !okPhase || !okLaunch || !okGeneration || !okCleanup || !okApproval || !okBinding || !oneOf(phase, "start-pending", "running", "stop-pending", "stopped") {
		return UntrustedSnapshot{}, ErrSchema
	}
	if binding != nil && (approval == nil || binding.Generation > generation) {
		return UntrustedSnapshot{}, ErrSchema
	}
	switch launch {
	case "none":
		if approval != nil || binding != nil || cleanup {
			return UntrustedSnapshot{}, ErrSchema
		}
	case "approval-reserved":
		if approval == nil || binding != nil || cleanup {
			return UntrustedSnapshot{}, ErrSchema
		}
	case "binding-issued", "process-started":
		if approval == nil || binding == nil || cleanup {
			return UntrustedSnapshot{}, ErrSchema
		}
	case "quiescing":
		if cleanup {
			return UntrustedSnapshot{}, ErrSchema
		}
	case "stopped-confirmed":
		if !cleanup {
			return UntrustedSnapshot{}, ErrSchema
		}
	case "failed":
	default:
		return UntrustedSnapshot{}, ErrSchema
	}
	return UntrustedSnapshot{ServicePhase: phase, Generation: generation, ApprovalRequestID: approval, LaunchPhase: launch, Binding: binding, WorkloadCleanupConfirmed: cleanup}, nil
}

// UntrustedIdentityは既存control/1と同形の相関値で、認証済みidentityではない。
type UntrustedIdentity struct {
	LaunchID     string `json:"launchId"`
	Epoch        uint64 `json:"epoch,string"`
	PolicyDigest string `json:"policyDigest"`
}
type UntrustedBinding struct {
	Generation uint64            `json:"generation,string"`
	Identity   UntrustedIdentity `json:"identity"`
}

// UntrustedSnapshotのphaseとcleanupは送信者の主張で、SCMや回収の証明ではない。
type UntrustedSnapshot struct {
	ServicePhase             string            `json:"servicePhase"`
	Generation               uint64            `json:"generation,string"`
	ApprovalRequestID        *string           `json:"approvalRequestId"`
	LaunchPhase              string            `json:"launchPhase"`
	Binding                  *UntrustedBinding `json:"binding"`
	WorkloadCleanupConfirmed bool              `json:"workloadCleanupConfirmed"`
}

// UntrustedIntentは内容相関済みの表示候補で、現在の承認対象や期限内である証明ではない。
type UntrustedIntent struct {
	IntentID            string           `json:"intentId"`
	PreviewDigest       string           `json:"previewDigest"`
	IntentExpiresUnixMs int64            `json:"intentExpiresUnixMs,string"`
	Preview             UntrustedPreview `json:"preview"`
}

func intentShape(body map[string]any, boot string) (UntrustedIntent, error) {
	if !exactFields(body, "intentId", "previewDigest", "intentExpiresUnixMs", "preview") {
		return UntrustedIntent{}, ErrSchema
	}
	intent, okIntent := lowerHex(body["intentId"], 32)
	digest, okDigest := lowerHex(body["previewDigest"], 64)
	expires, okExpires := positiveUnixMs(body["intentExpiresUnixMs"])
	object, okObject := body["preview"].(map[string]any)
	if !okIntent || !okDigest || !okExpires || !okObject {
		return UntrustedIntent{}, ErrSchema
	}
	preview, err := previewShape(object)
	if err != nil || intent != preview.IntentID || boot != preview.BootEpoch || expires > preview.HardDeadlineUnixMs {
		return UntrustedIntent{}, ErrSchema
	}
	computed, err := PreviewDigest(preview)
	if err != nil || digest != computed {
		return UntrustedIntent{}, ErrSchema
	}
	return UntrustedIntent{IntentID: intent, PreviewDigest: digest, IntentExpiresUnixMs: expires, Preview: preview}, nil
}

// UntrustedApprovalは受付応答の相関値であり、binding発行やprocess開始の証拠ではない。
type UntrustedApproval struct {
	ApprovalRequestID string `json:"approvalRequestId"`
}

// UntrustedQuiesceはwire上の主張だけを保持し、owner回収やSCM停止を証明しない。
type UntrustedQuiesce struct {
	RevocationApplied        bool `json:"revocationApplied"`
	WorkloadCleanupConfirmed bool `json:"workloadCleanupConfirmed"`
}

// UntrustedServiceErrorは固定codeだけを持つ。codecでの認識は送信許可ではない。
type UntrustedServiceError struct {
	Code string `json:"code"`
}

func serviceErrorCode(code string) bool {
	return oneOf(code, "SERVICE_BAD_FRAME", "SERVICE_UNSUPPORTED_VERSION", "SERVICE_AUTH_DENIED", "SERVICE_NOT_READY", "SERVICE_UNCONFIGURED", "SERVICE_STALE_CONTEXT", "SERVICE_INTENT_EXPIRED", "SERVICE_INTENT_CONSUMED", "SERVICE_CONFIG_CHANGED", "SERVICE_REPLAY_DENIED", "SERVICE_STOPPING", "SERVICE_CLEANUP_UNCONFIRMED", "SERVICE_INTERNAL_FAILURE", "SERVICE_REQUEST_LIMIT")
}

// EncodeResponseは有限な未認証値をwire化するだけで、producerの送信権限を与えない。
func EncodeResponse(response UntrustedResponse) ([]byte, error) {
	// string/slice上限と単一variantを先に検査し、無制限の値をmarshalしない。
	if !boundedResponse(response) {
		return nil, ErrSchema
	}
	object := map[string]any{"protocol": Protocol, "type": response.Type, "bootEpoch": response.BootEpoch, "channelNonce": response.ChannelNonce, "sequence": strconv.FormatUint(response.Sequence, 10), "requestId": response.RequestID}
	if response.Type == "hello" {
		object["clientNonce"] = response.ClientNonce
		object["requestLimit"] = strconv.FormatUint(response.RequestLimit, 10)
	} else {
		object["command"] = response.Command
		object["result"] = response.Result
		switch {
		case response.Snapshot != nil:
			object["body"] = response.Snapshot
		case response.Intent != nil:
			object["body"] = response.Intent
		case response.Approval != nil:
			object["body"] = response.Approval
		case response.Quiesce != nil:
			object["body"] = response.Quiesce
		case response.Error != nil:
			object["body"] = response.Error
		}
	}
	frame, err := json.Marshal(object)
	if err != nil {
		return nil, ErrSchema
	}
	frame = append(frame, '\n')
	if _, err = DecodeUntrustedResponseFrame(frame); err != nil {
		return nil, err
	}
	return frame, nil
}
