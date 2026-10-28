package servicecontrol

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// UntrustedPreviewは表示候補のwire値。config/policy/keysetの正本との一致は証明しない。
type UntrustedPreview struct {
	BootEpoch              string                     `json:"bootEpoch"`
	IntentID               string                     `json:"intentId"`
	ConfigDigest           string                     `json:"configDigest"`
	PolicyDigest           string                     `json:"policyDigest"`
	KeySetDigest           string                     `json:"keySetDigest"`
	AllowedKeyFingerprints []string                   `json:"allowedKeyFingerprints"`
	Target                 UntrustedPreviewTarget     `json:"target"`
	PermissionSummary      UntrustedPermissionSummary `json:"permissionSummary"`
	HardDeadlineUnixMs     int64                      `json:"hardDeadlineUnixMs,string"`
}
type UntrustedPreviewTarget struct {
	Host         string `json:"host"`
	Port         string `json:"port"`
	OuterChannel string `json:"outerChannel"`
}
type UntrustedPermissionSummary struct {
	PolicyMode    string   `json:"policyMode"`
	GuestClass    string   `json:"guestClass"`
	OuterChannels []string `json:"outerChannels"`
}

// CanonicalPreviewは固定順のASCII JSONを返す。LFや認証根拠は追加しない。
func CanonicalPreview(preview UntrustedPreview) ([]byte, error) {
	if !validPreview(preview) {
		return nil, ErrSchema
	}
	return json.Marshal(preview)
}

// PreviewDigestは内容の相関値だけを生成し、署名・同意・鍵setの真正性を証明しない。
func PreviewDigest(preview UntrustedPreview) (string, error) {
	raw, err := CanonicalPreview(preview)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(Protocol+":preview\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}
func validPreview(p UntrustedPreview) bool {
	for _, s := range []string{p.BootEpoch, p.ConfigDigest, p.PolicyDigest, p.KeySetDigest} {
		if _, ok := lowerHex(s, 64); !ok {
			return false
		}
	}
	if _, ok := lowerHex(p.IntentID, 32); !ok {
		return false
	}
	if p.HardDeadlineUnixMs <= 0 || len(p.AllowedKeyFingerprints) < 1 || len(p.AllowedKeyFingerprints) > 8 {
		return false
	}
	for i, s := range p.AllowedKeyFingerprints {
		if len(s) != 50 || !strings.HasPrefix(s, "SHA256:") {
			return false
		}
		raw, err := base64.RawStdEncoding.Strict().DecodeString(s[7:])
		if err != nil || len(raw) != 32 || base64.RawStdEncoding.EncodeToString(raw) != s[7:] || i > 0 && p.AllowedKeyFingerprints[i-1] >= s {
			return false
		}
	}
	return p.Target == (UntrustedPreviewTarget{Host: "127.0.0.1", Port: "22", OuterChannel: "direct-tcpip"}) && oneOf(p.PermissionSummary.PolicyMode, "restricted", "full") && p.PermissionSummary.GuestClass == "nonadmin" && len(p.PermissionSummary.OuterChannels) == 1 && p.PermissionSummary.OuterChannels[0] == "direct-tcpip"
}
func previewShape(object map[string]any) (UntrustedPreview, error) {
	if !exactFields(object, "bootEpoch", "intentId", "configDigest", "policyDigest", "keySetDigest", "allowedKeyFingerprints", "target", "permissionSummary", "hardDeadlineUnixMs") {
		return UntrustedPreview{}, ErrSchema
	}
	target, okTarget := object["target"].(map[string]any)
	permission, okPermission := object["permissionSummary"].(map[string]any)
	keys, okKeys := object["allowedKeyFingerprints"].([]any)
	if !okTarget || !exactFields(target, "host", "port", "outerChannel") || !okPermission || !exactFields(permission, "policyMode", "guestClass", "outerChannels") || !okKeys || len(keys) < 1 || len(keys) > 8 {
		return UntrustedPreview{}, ErrSchema
	}
	channels, okChannels := permission["outerChannels"].([]any)
	if !okChannels || len(channels) != 1 {
		return UntrustedPreview{}, ErrSchema
	}
	deadline, okDeadline := positiveUnixMs(object["hardDeadlineUnixMs"])
	if !okDeadline {
		return UntrustedPreview{}, ErrSchema
	}
	// 下のstring assertionは型不一致を空文字へ落とし、validPreviewで必ず拒否する。
	p := UntrustedPreview{HardDeadlineUnixMs: deadline}
	p.BootEpoch, _ = object["bootEpoch"].(string)
	p.IntentID, _ = object["intentId"].(string)
	p.ConfigDigest, _ = object["configDigest"].(string)
	p.PolicyDigest, _ = object["policyDigest"].(string)
	p.KeySetDigest, _ = object["keySetDigest"].(string)
	p.Target.Host, _ = target["host"].(string)
	p.Target.Port, _ = target["port"].(string)
	p.Target.OuterChannel, _ = target["outerChannel"].(string)
	p.PermissionSummary.PolicyMode, _ = permission["policyMode"].(string)
	p.PermissionSummary.GuestClass, _ = permission["guestClass"].(string)
	channel, ok := channels[0].(string)
	if !ok {
		return UntrustedPreview{}, ErrSchema
	}
	p.PermissionSummary.OuterChannels = []string{channel}
	for _, value := range keys {
		s, ok := value.(string)
		if !ok {
			return UntrustedPreview{}, ErrSchema
		}
		p.AllowedKeyFingerprints = append(p.AllowedKeyFingerprints, s)
	}
	if !validPreview(p) {
		return UntrustedPreview{}, ErrSchema
	}
	return p, nil
}
func positiveUnixMs(value any) (int64, bool) {
	s, ok := value.(string)
	if !ok || len(s) < 1 || len(s) > 19 {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}
