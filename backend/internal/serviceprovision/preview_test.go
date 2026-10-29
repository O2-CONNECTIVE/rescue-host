package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type previewMemoryScope struct {
	rechecks, closes int
	err              error
}

func (s *previewMemoryScope) Recheck(ctx context.Context) error {
	s.rechecks++
	return errors.Join(s.err, ctx.Err())
}
func (s *previewMemoryScope) Close(context.Context) error { s.closes++; return nil }

func previewPreparedFixture(t *testing.T) (*Prepared, *subjectFaultKernel, *previewMemoryScope) {
	t.Helper()
	a := rootAnchor()
	k := provisionRecordKernel(a)
	r, _ := NewNativeRoot(a)
	r.api = k
	loaded, err := r.Open(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	scope := &previewMemoryScope{}
	p := &Prepared{loaded: loaded, scope: scope}
	p.accepted.Store(true)
	t.Cleanup(func() {
		if err := p.Close(rootContext(t)); err != nil && !errors.Is(err, ErrRootFatal) {
			t.Error(err)
		}
	})
	return p, k, scope
}

func TestProtectedRecordResolvesExistingPolicyKeysAndFrozenMaximum(t *testing.T) {
	p, _, scope := previewPreparedFixture(t)
	descriptor, err := p.PreviewDescriptor(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ConfigDigest, PolicyDigest, KeySetDigest, PolicyMode string
		AllowedKeyFingerprints                               []string
		MaximumDurationNs, LeaseCeilingNs                    string
	}
	raw, err := os.ReadFile("../../servicehost/testdata/protected-policy-preview.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(descriptor.ConfigDigest[:]) != fixture.ConfigDigest || hex.EncodeToString(descriptor.PolicyDigest[:]) != fixture.PolicyDigest || hex.EncodeToString(descriptor.KeySetDigest[:]) != fixture.KeySetDigest || descriptor.PolicyMode != fixture.PolicyMode || len(descriptor.AllowedKeyFingerprints) != 1 || descriptor.AllowedKeyFingerprints[0] != fixture.AllowedKeyFingerprints[0] || descriptor.LeaseCeiling != time.Second || scope.rechecks != 1 {
		t.Fatal("protected record resolution differs from shared vector")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	fixed, _, err := resolvePreviewRecord([]byte(validJSON), Subject{ProcessUserSID: rootUserSID, ServiceSID: rootServiceSID, ServiceName: "SyntheticService"}, now)
	if err != nil || !fixed.HardDeadline.Equal(now.Add(20*time.Second)) {
		t.Fatal("maximum deadline was not frozen at resolution", err)
	}
	descriptor.AllowedKeyFingerprints[0] = "caller changed"
	again, err := p.PreviewDescriptor(rootContext(t))
	if err != nil || again.AllowedKeyFingerprints[0] != fixture.AllowedKeyFingerprints[0] {
		t.Fatal("descriptor output aliased protected record", err)
	}
}

func TestPreviewConfigDigestUsesExactProtectedBytes(t *testing.T) {
	raw := []byte(validJSON + "\n")
	now := time.Now()
	d, _, err := resolvePreviewRecord(raw, Subject{ProcessUserSID: rootUserSID, ServiceSID: rootServiceSID, ServiceName: "SyntheticService"}, now)
	if err != nil || d.ConfigDigest != sha256.Sum256(raw) || d.ConfigDigest == sha256.Sum256([]byte(validJSON)) {
		t.Fatal("preview remarshal changed protected checksum domain", err)
	}
}

func TestPreviewRecordRejectsForeignSubjectAndInvalidKeyWithoutFallback(t *testing.T) {
	for _, kind := range []string{"foreign-service", "foreign-user", "invalid-key"} {
		t.Run(kind, func(t *testing.T) {
			raw := []byte(validJSON)
			subject := Subject{ProcessUserSID: rootUserSID, ServiceSID: rootServiceSID, ServiceName: "SyntheticService"}
			switch kind {
			case "foreign-service":
				subject.ServiceName = "OtherService"
			case "foreign-user":
				subject.ProcessUserSID = "S-1-5-21-1-2-3-9999"
			default:
				raw = []byte(strings.Replace(validJSON, "AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea", "invalid", 1))
			}
			if _, _, err := resolvePreviewRecord(raw, subject, time.Now()); err == nil {
				t.Fatal("invalid protected input generated a preview")
			}
		})
	}
}

func TestPreviewCompositionRejectsClosedFatalAndDriftedScope(t *testing.T) {
	for _, kind := range []string{"closed", "fatal", "scope"} {
		t.Run(kind, func(t *testing.T) {
			p, k, scope := previewPreparedFixture(t)
			switch kind {
			case "closed":
				if err := p.Close(rootContext(t)); err != nil {
					t.Fatal(err)
				}
			case "fatal":
				k.thread++
			default:
				scope.err = ErrRootDenied
			}
			if service, err := p.ComposePreviewService(rootContext(t), PreviewLimits{RequestLimit: 4, LaunchLimit: 5, IntentTTL: time.Second}); service != nil || err == nil {
				t.Fatal("unverified source composed preview service")
			}
		})
	}
}

func TestProtectedPreviewCompositionUsesReadOnlyTokenWithoutApprovalOrLaunch(t *testing.T) {
	p, _, scope := previewPreparedFixture(t)
	service, err := p.ComposePreviewService(rootContext(t), PreviewLimits{RequestLimit: 4, LaunchLimit: 5, IntentTTL: time.Second})
	if err != nil || service == nil {
		t.Fatal("protected source did not reach real preview constructor", err)
	}
	snapshot := service.Snapshot()
	if snapshot.Generation != 0 || snapshot.HasAuthority || snapshot.InstanceEpoch != ([32]byte{}) || scope.rechecks != 1 {
		t.Fatal("preview construction invented approval")
	}
	if err = service.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewKeySetAndFingerprintsUseSameDeduplicatedRawKeys(t *testing.T) {
	subject := Subject{ProcessUserSID: rootUserSID, ServiceSID: rootServiceSID, ServiceName: "SyntheticService"}
	now := time.Now()
	first, _, err := resolvePreviewRecord([]byte(validJSON), subject, now)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal([]byte(validJSON), &object); err != nil {
		t.Fatal(err)
	}
	backend := object["backend"].(map[string]any)
	key := backend["allowedKeys"].([]any)[0]
	backend["allowedKeys"] = []any{key, key}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, _, err := resolvePreviewRecord(raw, subject, now)
	if err != nil || duplicate.KeySetDigest != first.KeySetDigest || len(duplicate.AllowedKeyFingerprints) != 1 || duplicate.AllowedKeyFingerprints[0] != first.AllowedKeyFingerprints[0] || duplicate.ConfigDigest == first.ConfigDigest {
		t.Fatal("keyset and fingerprints diverged or record bytes were normalized", err)
	}
}

func TestCanceledProtectedPreviewDoesNotReturnDescriptor(t *testing.T) {
	p, _, scope := previewPreparedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.PreviewDescriptor(ctx); err == nil || scope.rechecks != 0 {
		t.Fatal("canceled source query reached scope or returned descriptor")
	}
}
