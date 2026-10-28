package bootstrap

import (
	"encoding/base64"
	"slices"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

func validMessage(m Message) bool {
	g, c := m.Grant, m.Config
	id := g.Binding.Identity
	wid := backend.Identity{LaunchID: m.Wire.Identity.LaunchID, Epoch: m.Wire.Identity.Epoch, PolicyDigest: m.Wire.Identity.PolicyDigest}
	_, epochOK := decimal(id.Epoch, 64, false)
	if !isDigest(m.IssuerInstanceEpoch) || g.Binding.Generation == 0 || !epochOK || id != wid || m.Wire.Generation != strconv.FormatUint(g.Binding.Generation, 10) || g.GuestBoundary != supervisor.SeparatedNonAdminGuest || g.KeySetDigest == ([32]byte{}) || g.HardDeadline.IsZero() || g.BackendLeaseCeiling <= 0 || !asciiToken(m.HostKeyRef) {
		return false
	}
	// 現在時刻を読まず、絶対期限を相対時間に再基準化しない。
	deadline := g.HardDeadline.UTC().Format(time.RFC3339Nano)
	if _, err := time.Parse(time.RFC3339Nano, deadline); err != nil {
		return false
	}
	if _, err := control.NewState(m.Wire); err != nil {
		return false
	}
	policy, err := backend.PolicyDigest(c.OperationMode)
	if err != nil || policy != id.PolicyDigest || c.MaximumDuration <= 0 || c.MaximumDuration > 2*time.Hour || c.LeaseDuration <= 0 || c.LeaseDuration > time.Minute || c.LeaseDuration > c.MaximumDuration || c.LeaseDuration > g.BackendLeaseCeiling || c.MaxChannels < 1 || c.MaxChannels > 32 || c.MaxHandshakes < 1 || c.MaxHandshakes > 64 || c.MaxTransports < 1 || c.MaxTransports > 64 || m.Limits.FrameBytes != RecordBytes || m.Limits.Frames < 1 || int64(m.Limits.Frames) > 2147483647 || m.Limits.CleanupTimeout <= 0 || len(c.AllowedKeys) < 1 || len(c.AllowedKeys) > 8 {
		return false
	}
	keys := make([]ssh.PublicKey, 0, len(c.AllowedKeys))
	for _, value := range c.AllowedKeys {
		if len(value) != 68 {
			return false
		}
		data, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || base64.StdEncoding.EncodeToString(data) != value {
			return false
		}
		key, err := ssh.ParsePublicKey(data)
		if err != nil || key.Type() != ssh.KeyAlgoED25519 || base64.StdEncoding.EncodeToString(key.Marshal()) != value {
			return false
		}
		keys = append(keys, key)
	}
	digest, err := runbackend.KeySetDigest(keys)
	return err == nil && digest == g.KeySetDigest
}

func matches(m Message, e Expected) bool {
	g, w := m.Grant, e.Grant
	return m.IssuerInstanceEpoch == e.IssuerInstanceEpoch && g.Binding == w.Binding && g.KeySetDigest == w.KeySetDigest && g.GuestBoundary == w.GuestBoundary && g.HardDeadline.Equal(w.HardDeadline) && g.BackendLeaseCeiling == w.BackendLeaseCeiling && m.Wire == e.Wire && m.HostKeyRef == e.HostKeyRef && m.Limits == e.Limits && m.Config.OperationMode == e.Config.OperationMode && m.Config.MaximumDuration == e.Config.MaximumDuration && m.Config.LeaseDuration == e.Config.LeaseDuration && m.Config.MaxChannels == e.Config.MaxChannels && m.Config.MaxHandshakes == e.Config.MaxHandshakes && m.Config.MaxTransports == e.Config.MaxTransports && slices.Equal(m.Config.AllowedKeys, e.Config.AllowedKeys)
}

func decimal(s string, bits int, positive bool) (uint64, bool) {
	if s == "" || len(s) > 20 || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, bits)
	return n, err == nil && (!positive || n > 0)
}
func isDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func asciiToken(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
