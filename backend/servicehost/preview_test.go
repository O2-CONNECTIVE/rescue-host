package servicehost

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

func previewDescriptorFixture(t *testing.T) PreviewDescriptor {
	t.Helper()
	raw, err := os.ReadFile("testdata/protected-policy-preview.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ConfigDigest           string   `json:"configDigest"`
		PolicyDigest           string   `json:"policyDigest"`
		KeySetDigest           string   `json:"keySetDigest"`
		AllowedKeyFingerprints []string `json:"allowedKeyFingerprints"`
		PolicyMode             string   `json:"policyMode"`
		MaximumDurationNs      string   `json:"maximumDurationNs"`
		LeaseCeilingNs         string   `json:"leaseCeilingNs"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	digest := func(value string) (out [32]byte) {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			t.Fatal("invalid shared fixture digest")
		}
		copy(out[:], decoded)
		return
	}
	maximum, err := strconv.ParseInt(fixture.MaximumDurationNs, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := strconv.ParseInt(fixture.LeaseCeilingNs, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return PreviewDescriptor{ConfigDigest: digest(fixture.ConfigDigest), PolicyDigest: digest(fixture.PolicyDigest), KeySetDigest: digest(fixture.KeySetDigest), AllowedKeyFingerprints: fixture.AllowedKeyFingerprints, PolicyMode: fixture.PolicyMode, HardDeadline: time.Now().Add(time.Duration(maximum)), LeaseCeiling: time.Duration(lease)}
}
func previewConfigFixture(t *testing.T) PreviewServiceConfig {
	return PreviewServiceConfig{Ready: controlFactoryConfig(t), Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Second}, RequestLimit: 3, LaunchLimit: 4, IntentTTL: time.Second, Preview: previewDescriptorFixture(t)}
}
func TestNewPreviewServiceBuildsOnlyConcreteQueryCompositionWithoutLegacyProviders(t *testing.T) {
	c := previewConfigFixture(t)
	first, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	if first.cfg.Provisioning != nil || first.cfg.Launcher != nil || first.prepare != nil || first.control == nil || !first.control.factories.canonical() || first.auth == nil {
		t.Fatal("preview installed a dummy legacy provider or omitted concrete controlled composition")
	}
	if first.issuer != nil || first.started {
		t.Fatal("constructor acquired or issued authority")
	}
	nonce := first.dispatch.profile.ChannelNonce
	if !dispatchHex(nonce, 64) || nonce == second.dispatch.profile.ChannelNonce {
		t.Fatal("service nonce was missing or reused")
	}
	c.Preview.AllowedKeyFingerprints[0] = "caller mutation"
	if first.preview.descriptor.AllowedKeyFingerprints[0] == c.Preview.AllowedKeyFingerprints[0] {
		t.Fatal("preview descriptor borrowed caller slice")
	}
	if s, err := NewService(ServiceConfig{Ready: c.Ready, Budgets: c.Budgets}); s != nil || err == nil {
		t.Fatal("preview weakened legacy nil-provider rejection")
	}
}

func TestNewPreviewServiceRejectsInvalidDisplayDescriptorAndLimits(t *testing.T) {
	for _, kind := range []string{"config-zero", "policy-zero", "keyset-zero", "mode-mismatch", "mode-unknown", "fingerprint-missing", "fingerprint-malformed", "fingerprint-duplicate", "fingerprint-unsorted", "deadline-past", "lease-zero", "lease-too-large", "ttl-zero", "ttl-negative", "request-zero", "insufficient-launch", "launch-too-large", "budget-zero", "ready-missing"} {
		t.Run(kind, func(t *testing.T) {
			c := previewConfigFixture(t)
			switch kind {
			case "config-zero":
				c.Preview.ConfigDigest = [32]byte{}
			case "policy-zero":
				c.Preview.PolicyDigest = [32]byte{}
			case "keyset-zero":
				c.Preview.KeySetDigest = [32]byte{}
			case "mode-mismatch":
				c.Preview.PolicyMode = "full"
			case "mode-unknown":
				c.Preview.PolicyMode = "admin"
			case "fingerprint-missing":
				c.Preview.AllowedKeyFingerprints = nil
			case "fingerprint-malformed":
				c.Preview.AllowedKeyFingerprints[0] = "SHA256:broken"
			case "fingerprint-duplicate":
				c.Preview.AllowedKeyFingerprints = append(c.Preview.AllowedKeyFingerprints, c.Preview.AllowedKeyFingerprints[0])
			case "fingerprint-unsorted":
				c.Preview.AllowedKeyFingerprints = append(c.Preview.AllowedKeyFingerprints, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			case "deadline-past":
				c.Preview.HardDeadline = time.Now().Add(-time.Second)
			case "lease-zero":
				c.Preview.LeaseCeiling = 0
			case "lease-too-large":
				c.Preview.LeaseCeiling = time.Minute + time.Nanosecond
			case "ttl-zero":
				c.IntentTTL = 0
			case "ttl-negative":
				c.IntentTTL = -time.Second
			case "request-zero":
				c.RequestLimit = 0
			case "insufficient-launch":
				c.LaunchLimit = c.RequestLimit
			case "launch-too-large":
				c.LaunchLimit = 1025
			case "budget-zero":
				c.Budgets.Cleanup = 0
			case "ready-missing":
				c.Ready.PrimaryToken = 0
			}
			if s, err := NewPreviewService(c); s != nil || err == nil {
				t.Fatal("invalid preview configuration accepted")
			}
		})
	}
	// LeaseCeiling is a display cap: construction elapsed time must not impose
	// another Lease <= remaining-HardDeadline check or silently mint a grant.
	c := previewConfigFixture(t)
	c.Preview.LeaseCeiling = time.Minute
	if _, err := NewPreviewService(c); err != nil {
		t.Fatal("valid display cap was compared to elapsed remaining time", err)
	}
}
