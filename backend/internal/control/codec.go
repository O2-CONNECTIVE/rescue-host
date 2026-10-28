package control

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"
)

const Protocol = "rescue-host-control/1"

type Code string

const (
	InvalidMessage         Code = "INVALID_MESSAGE"
	VersionMismatch        Code = "VERSION_MISMATCH"
	ContextMismatch        Code = "CONTEXT_MISMATCH"
	RequestRejected        Code = "REQUEST_REJECTED"
	DeadlineExceeded       Code = "DEADLINE_EXCEEDED"
	TerminationUnconfirmed Code = "TERMINATION_UNCONFIRMED"
)

func (c Code) Error() string { return string(c) }

type WireIdentity struct {
	LaunchID     string `json:"launchId"`
	Epoch        string `json:"epoch"`
	PolicyDigest string `json:"policyDigest"`
}
type Binding struct {
	ArtifactSHA256 string `json:"artifactSha256"`
	ChannelNonce   string `json:"channelNonce"`
}
type Message struct {
	Protocol   string          `json:"protocol"`
	Kind       string          `json:"kind"`
	Generation string          `json:"generation"`
	Sequence   string          `json:"sequence,omitempty"`
	RequestID  string          `json:"requestId,omitempty"`
	Identity   WireIdentity    `json:"identity"`
	Binding    *Binding        `json:"binding,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

const FrameBytes = 16384

// Decode は共有正本のraw制約とshapeだけを検査する。認証や状態検査は行わない。
func Decode(frame []byte) (Message, error) {
	var message Message
	if len(frame) == 0 || len(frame) > FrameBytes || frame[len(frame)-1] != '\n' || bytes.IndexByte(frame[:len(frame)-1], '\n') >= 0 || !utf8.Valid(frame) {
		return message, InvalidMessage
	}
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.UseNumber()
	value, err := readValue(decoder, 1)
	if err != nil {
		return message, InvalidMessage
	}
	if _, err = decoder.Token(); err != io.EOF {
		return message, InvalidMessage
	}
	object, ok := value.(map[string]any)
	if !ok {
		return message, InvalidMessage
	}
	protocol, ok := object["protocol"].(string)
	if !ok {
		return message, InvalidMessage
	}
	if protocol != Protocol {
		return message, VersionMismatch
	}
	if !validShape(object) {
		return message, InvalidMessage
	}
	if err := json.Unmarshal(frame, &message); err != nil {
		return Message{}, InvalidMessage
	}
	return message, nil
}

// Encode にも同じ制約を適用し、内部の不正な結果をwireへ出さない。
func Encode(message Message) ([]byte, error) {
	// marshal前にも内部入力を制限し、巨大な値を先に確保しない。
	total := len(message.Payload)
	if total > FrameBytes {
		return nil, InvalidMessage
	}
	parts := []string{message.Protocol, message.Kind, message.Generation, message.Sequence, message.RequestID, message.Identity.LaunchID, message.Identity.Epoch, message.Identity.PolicyDigest}
	if message.Binding != nil {
		parts = append(parts, message.Binding.ArtifactSHA256, message.Binding.ChannelNonce)
	}
	for _, part := range parts {
		if len(part) > FrameBytes-total {
			return nil, InvalidMessage
		}
		total += len(part)
	}
	frame, err := json.Marshal(message)
	if err != nil {
		return nil, InvalidMessage
	}
	frame = append(frame, '\n')
	if _, err = Decode(frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func readValue(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, InvalidMessage
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if depth > 8 {
		return nil, InvalidMessage
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, InvalidMessage
			}
			name, ok := key.(string)
			if !ok {
				return nil, InvalidMessage
			}
			if _, duplicate := object[name]; duplicate {
				return nil, InvalidMessage
			}
			value, err := readValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, InvalidMessage
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := readValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, InvalidMessage
		}
		return array, nil
	default:
		return nil, InvalidMessage
	}
}

func exact(value any, names ...string) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok || len(object) != len(names) {
		return nil, false
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return nil, false
		}
	}
	return object, true
}

func decimal(value any, positive bool) bool {
	text, ok := value.(string)
	if !ok || text == "" || len(text) > 20 || (len(text) > 1 && text[0] == '0') {
		return false
	}
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}
	number, err := strconv.ParseUint(text, 10, 64)
	return err == nil && (!positive || number > 0)
}

func asciiToken(value any) bool {
	text, ok := value.(string)
	if !ok || len(text) < 1 || len(text) > 128 {
		return false
	}
	for _, char := range text {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func digest(value any) bool {
	text, ok := value.(string)
	if !ok || len(text) != 64 {
		return false
	}
	for _, char := range text {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func member(value any, values ...string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, candidate := range values {
		if text == candidate {
			return true
		}
	}
	return false
}

func validShape(object map[string]any) bool {
	id, ok := exact(object["identity"], "launchId", "epoch", "policyDigest")
	if !ok || !asciiToken(id["launchId"]) || !decimal(id["epoch"], false) || !digest(id["policyDigest"]) || !decimal(object["generation"], true) {
		return false
	}
	kind, ok := object["kind"].(string)
	if !ok {
		return false
	}
	if kind == "hello" || kind == "hello_ack" {
		if _, ok := exact(object, "protocol", "kind", "generation", "identity", "binding"); !ok {
			return false
		}
		binding, ok := exact(object["binding"], "artifactSha256", "channelNonce")
		return ok && digest(binding["artifactSha256"]) && digest(binding["channelNonce"])
	}
	fields := []string{"protocol", "kind", "generation", "sequence", "identity", "payload"}
	if kind != "state_changed" {
		fields = append(fields, "requestId")
		if !decimal(object["requestId"], true) {
			return false
		}
	}
	if _, ok := exact(object, fields...); !ok || !decimal(object["sequence"], true) {
		return false
	}
	switch kind {
	case "start":
		payload, ok := exact(object["payload"], "policyRef")
		return ok && asciiToken(payload["policyRef"])
	case "snapshot", "stop":
		_, ok := exact(object["payload"])
		return ok
	case "receipt":
		payload, ok := exact(object["payload"], "operation", "accepted")
		return ok && member(payload["operation"], "start", "stop") && payload["accepted"] == true
	case "snapshot_result", "state_changed":
		payload, ok := exact(object["payload"], "goState", "pending", "active", "owned", "goCloseConfirmed")
		if !ok || !member(payload["goState"], "prepared", "ready", "active", "revoking", "terminated") || !decimal(payload["pending"], false) || !decimal(payload["active"], false) || !decimal(payload["owned"], false) {
			return false
		}
		confirmed, ok := payload["goCloseConfirmed"].(bool)
		return ok && (!confirmed || payload["goState"] == "terminated")
	case "error":
		payload, ok := exact(object["payload"], "code", "retryable", "goCloseConfirmed")
		if !ok || !member(payload["code"], string(VersionMismatch), string(InvalidMessage), string(ContextMismatch), string(RequestRejected), string(DeadlineExceeded), string(TerminationUnconfirmed)) || payload["retryable"] != false {
			return false
		}
		_, ok = payload["goCloseConfirmed"].(bool)
		return ok
	default:
		return false
	}
}
