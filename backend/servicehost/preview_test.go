package servicehost

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"
)

func previewDescriptorFixture(t *testing.T) PreviewDescriptor {
	t.Helper()
	policy, err := hex.DecodeString("218fa8d3d295dccf58f443a934f4d3178f39d5c99c303829f5d6d090ec7a40a1")
	if err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	copy(digest[:], policy)
	// Synthetic raw Ed25519 public bytes; no private key or authentication proof.
	raw := [32]byte{1, 2, 3}
	fingerprint := sha256.Sum256(raw[:])
	return PreviewDescriptor{ConfigDigest: [32]byte{1}, PolicyDigest: digest, KeySetDigest: [32]byte{2}, AllowedKeyFingerprints: []string{"SHA256:" + base64.RawStdEncoding.EncodeToString(fingerprint[:])}, PolicyMode: "restricted", HardDeadline: time.Now().Add(time.Minute), LeaseCeiling: time.Second}
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
