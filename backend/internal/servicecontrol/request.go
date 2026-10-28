// Package servicecontrolはservice制御要求の未認証な構造だけを復号する。
// 成功は認証・今回の承認・dispatch・grant・実行許可ではない。
package servicecontrol

import (
	"errors"
	"strconv"
)

const Protocol = "rescue-host-service/1"

var (
	ErrFrame  = errors.New("service要求frameを受理できません")
	ErrSchema = errors.New("service要求の構造が不正です")
	ErrDepth  = errors.New("service要求JSONの深さ上限を超えました")
)

type RequestType string

const (
	Hello   RequestType = "hello"
	Request RequestType = "request"
)

type Command string

const (
	Status              Command = "status"
	PrepareIntent       Command = "prepare-intent"
	ConfirmApproval     Command = "confirm-approval"
	RevokeAllAndQuiesce Command = "revoke-all-and-quiesce"
)

// ConfirmationFieldsは要求内の相関値であり、承認済みの証拠ではない。
type ConfirmationFields struct{ IntentID, PreviewDigest string }

// UntrustedRequestはwire由来の値だけを持つ。callerが構築しても権限を得ない。
// BootEpochはissuer instanceの表現で、control/1 Identity.Epochへ流用しない。
type UntrustedRequest struct {
	Type                                            RequestType
	Sequence                                        uint64
	RequestID, ClientNonce, BootEpoch, ChannelNonce string
	Command                                         Command
	Body                                            ConfirmationFields
}

// DecodeUntrustedFrameは純粋な構造検証だけを行う。
func DecodeUntrustedFrame(frame []byte) (UntrustedRequest, error) {
	object, err := decodeFrame(frame)
	if err != nil {
		return UntrustedRequest{}, err
	}
	return requestShape(object)
}

func requestShape(object map[string]any) (UntrustedRequest, error) {
	if object["protocol"] != Protocol {
		return UntrustedRequest{}, ErrSchema
	}
	id, okID := lowerHex(object["requestId"], 32)
	if !okID {
		return UntrustedRequest{}, ErrSchema
	}
	if object["type"] == string(Hello) {
		nonce, okNonce := lowerHex(object["clientNonce"], 64)
		if !okNonce || object["sequence"] != "0" || !exactFields(object, "protocol", "type", "sequence", "requestId", "clientNonce") {
			return UntrustedRequest{}, ErrSchema
		}
		return UntrustedRequest{Type: Hello, RequestID: id, ClientNonce: nonce}, nil
	}
	if object["type"] != string(Request) || !exactFields(object, "protocol", "type", "bootEpoch", "channelNonce", "sequence", "requestId", "command", "body") {
		return UntrustedRequest{}, ErrSchema
	}
	epoch, okEpoch := lowerHex(object["bootEpoch"], 64)
	nonce, okNonce := lowerHex(object["channelNonce"], 64)
	sequence, okSequence := positiveDecimal(object["sequence"])
	body, okBody := object["body"].(map[string]any)
	command, okCommand := object["command"].(string)
	if !okEpoch || !okNonce || !okSequence || !okBody || !okCommand {
		return UntrustedRequest{}, ErrSchema
	}
	var confirmation ConfirmationFields
	switch Command(command) {
	case Status, PrepareIntent, RevokeAllAndQuiesce:
		if !exactFields(body) {
			return UntrustedRequest{}, ErrSchema
		}
	case ConfirmApproval:
		intent, okIntent := lowerHex(body["intentId"], 32)
		digest, okDigest := lowerHex(body["previewDigest"], 64)
		if !exactFields(body, "intentId", "previewDigest") || !okIntent || !okDigest {
			return UntrustedRequest{}, ErrSchema
		}
		confirmation = ConfirmationFields{IntentID: intent, PreviewDigest: digest}
	default:
		return UntrustedRequest{}, ErrSchema
	}
	return UntrustedRequest{Type: Request, Sequence: sequence, RequestID: id, BootEpoch: epoch, ChannelNonce: nonce, Command: Command(command), Body: confirmation}, nil
}

func positiveDecimal(value any) (uint64, bool) {
	s, ok := value.(string)
	if !ok || len(s) == 0 || len(s) > 20 {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != s {
		return 0, false
	}
	return n, true
}

func exactFields(object map[string]any, names ...string) bool {
	if len(object) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}
func lowerHex(value any, length int) (string, bool) {
	s, ok := value.(string)
	if !ok || len(s) != length {
		return "", false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return s, true
}
