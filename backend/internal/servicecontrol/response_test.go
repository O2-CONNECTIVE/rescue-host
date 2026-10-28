package servicecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type responseCase struct {
	ID                            string `json:"id"`
	FrameBase64                   string `json:"frameBase64"`
	ByteLength                    int    `json:"byteLength"`
	StructuralAccepted            bool   `json:"structuralAccepted"`
	AuthenticationEstablished     bool   `json:"authenticationEstablished"`
	OwnerProofEstablished         bool   `json:"ownerProofEstablished"`
	ProducerPermissionEstablished bool   `json:"producerPermissionEstablished"`
}
type responseVectorFile struct {
	Cases            []responseCase `json:"cases"`
	GenericDepthOnly []struct {
		ID                        string `json:"id"`
		FrameBase64               string `json:"frameBase64"`
		ByteLength                int    `json:"byteLength"`
		ParserDepthAccepted       bool   `json:"parserDepthAccepted"`
		ProtocolSchemaAccepted    bool   `json:"protocolSchemaAccepted"`
		AuthenticationEstablished bool   `json:"authenticationEstablished"`
	} `json:"genericDepthOnly"`
	CanonicalPreview []struct {
		ID            string           `json:"id"`
		Preview       UntrustedPreview `json:"preview"`
		CanonicalUTF8 string           `json:"canonicalUTF8"`
		SHA256        string           `json:"sha256"`
	} `json:"canonicalPreview"`
}

func responseVectors(t *testing.T) responseVectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/response-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v responseVectorFile
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestResponseHelloVector(t *testing.T) {
	v := responseVectors(t).Cases[0]
	if v.ID != "RSP-P01-server-hello" || !v.StructuralAccepted || v.AuthenticationEstablished || v.OwnerProofEstablished || v.ProducerPermissionEstablished {
		t.Fatal("unexpected untrusted fixture")
	}
	got, err := DecodeUntrustedResponseFrame(vectorBytes(t, v.FrameBase64, v.ByteLength))
	want := UntrustedResponse{Type: "hello", RequestID: strings.Repeat("1", 32), ClientNonce: strings.Repeat("f", 64), BootEpoch: strings.Repeat("a", 64), ChannelNonce: strings.Repeat("b", 64), RequestLimit: 1024}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("hello got=%+v err=%v", got, err)
	}
}

func TestResponseZeroGrantSnapshot(t *testing.T) {
	v := responseVectors(t).Cases[1]
	got, err := DecodeUntrustedResponseFrame(vectorBytes(t, v.FrameBase64, v.ByteLength))
	if err != nil || got.Type != "response" || got.Snapshot == nil || got.Snapshot.Generation != 0 || got.Snapshot.Binding != nil || got.Snapshot.ApprovalRequestID != nil || got.Snapshot.WorkloadCleanupConfirmed || got.Snapshot.ServicePhase != "running" || got.Snapshot.LaunchPhase != "none" {
		t.Fatalf("snapshot got=%+v err=%v", got, err)
	}
}

func TestResponseCanonicalPreviewVectors(t *testing.T) {
	cases := responseVectors(t).CanonicalPreview
	if len(cases) != 2 {
		t.Fatal("canonical preview vector inventory")
	}
	for _, v := range cases {
		t.Run(v.ID, func(t *testing.T) {
			got, err := CanonicalPreview(v.Preview)
			digest, digestErr := PreviewDigest(v.Preview)
			if err != nil || digestErr != nil || string(got) != v.CanonicalUTF8 || digest != v.SHA256 {
				t.Fatalf("canonical preview mismatch: err=%v digestErr=%v digest=%s", err, digestErr, digest)
			}
		})
	}
}

func responseFixture(t *testing.T, id string) []byte {
	t.Helper()
	for _, v := range responseVectors(t).Cases {
		if v.ID == id {
			return vectorBytes(t, v.FrameBase64, v.ByteLength)
		}
	}
	t.Fatal("missing response fixture")
	return nil
}
func TestResponsePreparedIntent(t *testing.T) {
	got, err := DecodeUntrustedResponseFrame(responseFixture(t, "RSP-P10-intent-prepared"))
	if err != nil || got.Intent == nil || got.Intent.IntentID != strings.Repeat("2", 32) || got.Intent.PreviewDigest != "374df8f5bcef67ca9c39846ff7e72d2c478814746c9ebc79537921b4a3ba23a0" || got.Intent.IntentExpiresUnixMs != 2000000005000 {
		t.Fatalf("prepared intent got=%+v err=%v", got, err)
	}
}

func TestResponseApprovalReceipt(t *testing.T) {
	got, err := DecodeUntrustedResponseFrame(responseFixture(t, "RSP-P11-approval-receipt-not-launch-proof"))
	if err != nil || got.Approval == nil || got.Approval.ApprovalRequestID != got.RequestID || got.Snapshot != nil || got.Intent != nil {
		t.Fatalf("approval receipt got=%+v err=%v", got, err)
	}
}

func TestResponseQuiesceClaims(t *testing.T) {
	for _, tc := range []struct {
		id      string
		cleanup bool
	}{
		{"RSP-P12-quiesce-accepted-not-cleanup-proof", false},
		{"RSP-P13-workload-quiesced-claim-not-service-stopped", true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, err := DecodeUntrustedResponseFrame(responseFixture(t, tc.id))
			if err != nil || got.Quiesce == nil || !got.Quiesce.RevocationApplied || got.Quiesce.WorkloadCleanupConfirmed != tc.cleanup || got.Snapshot != nil {
				t.Fatalf("quiesce claim got=%+v err=%v", got, err)
			}
		})
	}
}

func TestResponseFixedError(t *testing.T) {
	got, err := DecodeUntrustedResponseFrame(responseFixture(t, "RSP-P14-fixed-error"))
	if err != nil || got.Error == nil || got.Error.Code != "SERVICE_INTENT_EXPIRED" || got.Quiesce != nil {
		t.Fatalf("fixed error got=%+v err=%v", got, err)
	}
}

func TestResponseAllFixedVectors(t *testing.T) {
	positives, negatives := 0, 0
	seen := map[string]bool{}
	for _, v := range responseVectors(t).Cases {
		if seen[v.ID] {
			t.Fatal("duplicate vector ID")
		}
		seen[v.ID] = true
		if v.StructuralAccepted {
			positives++
		} else {
			negatives++
		}
		t.Run(v.ID, func(t *testing.T) {
			if v.AuthenticationEstablished || v.OwnerProofEstablished || v.ProducerPermissionEstablished {
				t.Fatal("structural fixture cannot establish authority")
			}
			got, err := DecodeUntrustedResponseFrame(vectorBytes(t, v.FrameBase64, v.ByteLength))
			if (err == nil) != v.StructuralAccepted {
				t.Fatalf("acceptance mismatch: err=%v", err)
			}
			if err != nil && !reflect.DeepEqual(got, UntrustedResponse{}) {
				t.Fatal("partial response escaped on failure")
			}
		})
	}
	if positives != 17 || negatives != 68 {
		t.Fatalf("vector inventory positive=%d negative=%d", positives, negatives)
	}
}

func TestResponseEncoderPreservesEveryPositiveWireValue(t *testing.T) {
	for _, v := range responseVectors(t).Cases {
		if !v.StructuralAccepted {
			continue
		}
		t.Run(v.ID, func(t *testing.T) {
			original := vectorBytes(t, v.FrameBase64, v.ByteLength)
			decoded, err := DecodeUntrustedResponseFrame(original)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := EncodeResponse(decoded)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if json.Unmarshal(original, &want) != nil || json.Unmarshal(encoded, &got) != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("encoder lost or changed a fixed wire value")
			}
			again, err := DecodeUntrustedResponseFrame(encoded)
			if err != nil || !reflect.DeepEqual(again, decoded) || len(encoded) > FrameBytes || encoded[len(encoded)-1] != '\n' {
				t.Fatal("encoded response is not the same bounded untrusted value")
			}
		})
	}
}

func TestResponseFixedVectorFileIsUnchanged(t *testing.T) {
	data, err := os.ReadFile("testdata/response-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "9905eccb3a7d646f357b502ebe27510d3e7b0bada2d5ef9275b24e01f49068d8" {
		t.Fatal("shared response vectors drifted")
	}
}
func TestResponseGenericDepthVectors(t *testing.T) {
	cases := responseVectors(t).GenericDepthOnly
	if len(cases) != 2 {
		t.Fatal("depth vector inventory")
	}
	for _, v := range cases {
		t.Run(v.ID, func(t *testing.T) {
			if v.ProtocolSchemaAccepted || v.AuthenticationEstablished {
				t.Fatal("generic depth fixture is not an authorized service response")
			}
			got, err := DecodeUntrustedResponseFrame(vectorBytes(t, v.FrameBase64, v.ByteLength))
			want := ErrDepth
			if v.ParserDepthAccepted {
				want = ErrSchema
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(got, UntrustedResponse{}) {
				t.Fatalf("depth failure stage: %v", err)
			}
		})
	}
}
func TestResponseEncoderRejectsMixedAndInvalidTypedVariants(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		mutate   func(*UntrustedResponse)
	}{
		{"empty value", "RSP-P01-server-hello", func(r *UntrustedResponse) { *r = UntrustedResponse{} }},
		{"hello response command", "RSP-P01-server-hello", func(r *UntrustedResponse) { r.Command = Status }},
		{"hello response body", "RSP-P01-server-hello", func(r *UntrustedResponse) { r.Snapshot = &UntrustedSnapshot{} }},
		{"hello result", "RSP-P01-server-hello", func(r *UntrustedResponse) { r.Result = "snapshot" }},
		{"hello nonzero sequence", "RSP-P01-server-hello", func(r *UntrustedResponse) { r.Sequence = 1 }},
		{"hello quota too large", "RSP-P01-server-hello", func(r *UntrustedResponse) { r.RequestLimit = 1025 }},
		{"response hello nonce", "RSP-P02-zero-grant-snapshot", func(r *UntrustedResponse) { r.ClientNonce = strings.Repeat("a", 64) }},
		{"response hello quota", "RSP-P02-zero-grant-snapshot", func(r *UntrustedResponse) { r.RequestLimit = 1 }},
		{"response missing body", "RSP-P02-zero-grant-snapshot", func(r *UntrustedResponse) { r.Snapshot = nil }},
		{"two variants", "RSP-P02-zero-grant-snapshot", func(r *UntrustedResponse) { r.Error = &UntrustedServiceError{Code: "SERVICE_NOT_READY"} }},
		{"mismatched body variant", "RSP-P02-zero-grant-snapshot", func(r *UntrustedResponse) { r.Command = ConfirmApproval; r.Result = "approval-accepted" }},
		{"false cleanup claim shape", "RSP-P12-quiesce-accepted-not-cleanup-proof", func(r *UntrustedResponse) { r.Quiesce.WorkloadCleanupConfirmed = true }},
		{"unknown code", "RSP-P14-fixed-error", func(r *UntrustedResponse) { r.Error.Code = "arbitrary diagnostic" }},
		{"unbounded id", "RSP-P01-server-hello", func(r *UntrustedResponse) { r.RequestID = strings.Repeat("a", FrameBytes*2) }},
		{"unbounded error", "RSP-P14-fixed-error", func(r *UntrustedResponse) { r.Error.Code = strings.Repeat("a", FrameBytes*2) }},
		{"unbounded launch id", "RSP-P04-binding-issued-epoch-zero", func(r *UntrustedResponse) { r.Snapshot.Binding.Identity.LaunchID = strings.Repeat("a", FrameBytes*2) }},
		{"nine fingerprints", "RSP-P10-intent-prepared", func(r *UntrustedResponse) { r.Intent.Preview.AllowedKeyFingerprints = make([]string, 9) }},
		{"huge fingerprint", "RSP-P10-intent-prepared", func(r *UntrustedResponse) {
			r.Intent.Preview.AllowedKeyFingerprints = []string{strings.Repeat("a", FrameBytes*2)}
		}},
		{"unsafe preview target", "RSP-P10-intent-prepared", func(r *UntrustedResponse) { r.Intent.Preview.Target.Host = "other" }},
		{"preview digest drift", "RSP-P10-intent-prepared", func(r *UntrustedResponse) { r.Intent.PreviewDigest = strings.Repeat("0", 64) }},
		{"wrong approval receipt", "RSP-P11-approval-receipt-not-launch-proof", func(r *UntrustedResponse) { r.Approval.ApprovalRequestID = strings.Repeat("0", 32) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := DecodeUntrustedResponseFrame(responseFixture(t, tc.id))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&r)
			frame, err := EncodeResponse(r)
			if err == nil || frame != nil {
				t.Fatal("invalid or mixed untrusted response encoded")
			}
		})
	}
}
func TestResponseValuesOwnTheirStorageAndKeepNoChannelState(t *testing.T) {
	for _, id := range []string{"RSP-P04-binding-issued-epoch-zero", "RSP-P10-intent-prepared"} {
		t.Run(id, func(t *testing.T) {
			frame := responseFixture(t, id)
			got, err := DecodeUntrustedResponseFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			clear(frame)
			again, err := DecodeUntrustedResponseFrame(responseFixture(t, id))
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatal("read buffer alias or hidden channel state")
			}
			if got.Intent != nil {
				got.Intent.Preview.AllowedKeyFingerprints[0] = "caller mutation"
				got.Intent.Preview.PermissionSummary.OuterChannels[0] = "caller mutation"
				if reflect.DeepEqual(got.Intent, again.Intent) {
					t.Fatal("decodes share mutable slices")
				}
			}
		})
	}
}
func TestResponseReservedErrorCodesAreOnlyUntrustedShapes(t *testing.T) {
	for _, code := range []string{"SERVICE_BAD_FRAME", "SERVICE_UNSUPPORTED_VERSION", "SERVICE_AUTH_DENIED", "SERVICE_NOT_READY", "SERVICE_UNCONFIGURED", "SERVICE_STALE_CONTEXT", "SERVICE_INTENT_EXPIRED", "SERVICE_INTENT_CONSUMED", "SERVICE_CONFIG_CHANGED", "SERVICE_REPLAY_DENIED", "SERVICE_STOPPING", "SERVICE_CLEANUP_UNCONFIRMED", "SERVICE_INTERNAL_FAILURE", "SERVICE_REQUEST_LIMIT"} {
		t.Run(code, func(t *testing.T) {
			r, err := DecodeUntrustedResponseFrame(responseFixture(t, "RSP-P14-fixed-error"))
			if err != nil {
				t.Fatal(err)
			}
			r.Error.Code = code
			frame, err := EncodeResponse(r)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeUntrustedResponseFrame(frame)
			if err != nil || got.Error == nil || got.Error.Code != code {
				t.Fatal("fixed code shape rejected")
			}
		})
	}
}

func TestCanonicalPreviewRejectsInvalidTypedValuesBeforeMarshaling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*UntrustedPreview)
	}{
		{"zero deadline", func(p *UntrustedPreview) { p.HardDeadlineUnixMs = 0 }},
		{"negative deadline", func(p *UntrustedPreview) { p.HardDeadlineUnixMs = -1 }},
		{"uppercase digest", func(p *UntrustedPreview) { p.ConfigDigest = strings.Repeat("A", 64) }},
		{"empty keys", func(p *UntrustedPreview) { p.AllowedKeyFingerprints = nil }},
		{"too many keys", func(p *UntrustedPreview) { p.AllowedKeyFingerprints = make([]string, 9) }},
		{"oversized key", func(p *UntrustedPreview) { p.AllowedKeyFingerprints = []string{strings.Repeat("a", FrameBytes*2)} }},
		{"padded key", func(p *UntrustedPreview) { p.AllowedKeyFingerprints[0] += "=" }},
		{"noncanonical pad bits", func(p *UntrustedPreview) { p.AllowedKeyFingerprints[0] = "SHA256:" + strings.Repeat("A", 42) + "B" }},
		{"duplicate keys", func(p *UntrustedPreview) { p.AllowedKeyFingerprints[1] = p.AllowedKeyFingerprints[0] }},
		{"unsorted keys", func(p *UntrustedPreview) {
			p.AllowedKeyFingerprints[0], p.AllowedKeyFingerprints[1] = p.AllowedKeyFingerprints[1], p.AllowedKeyFingerprints[0]
		}},
		{"another target", func(p *UntrustedPreview) { p.Target.Host = "other" }},
		{"another channel", func(p *UntrustedPreview) { p.PermissionSummary.OuterChannels = []string{"session"} }},
		{"admin guest", func(p *UntrustedPreview) { p.PermissionSummary.GuestClass = "admin" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := responseVectors(t).CanonicalPreview[0].Preview
			tc.mutate(&p)
			raw, err := CanonicalPreview(p)
			digest, digestErr := PreviewDigest(p)
			if err == nil || raw != nil || digestErr == nil || digest != "" {
				t.Fatal("invalid preview canonicalized")
			}
		})
	}
}

func TestResponseNestedFieldsRemainExact(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		mutate   func(map[string]any)
	}{
		{"binding unknown", "RSP-P04-binding-issued-epoch-zero", func(m map[string]any) { m["binding"].(map[string]any)["extra"] = true }},
		{"identity unknown", "RSP-P04-binding-issued-epoch-zero", func(m map[string]any) { m["binding"].(map[string]any)["identity"].(map[string]any)["extra"] = true }},
		{"preview target unknown", "RSP-P10-intent-prepared", func(m map[string]any) { m["preview"].(map[string]any)["target"].(map[string]any)["extra"] = true }},
		{"preview permission unknown", "RSP-P10-intent-prepared", func(m map[string]any) {
			m["preview"].(map[string]any)["permissionSummary"].(map[string]any)["extra"] = true
		}},
		{"missing explicit approval null", "RSP-P02-zero-grant-snapshot", func(m map[string]any) { delete(m, "approvalRequestId") }},
		{"missing snapshot cleanup", "RSP-P02-zero-grant-snapshot", func(m map[string]any) { delete(m, "workloadCleanupConfirmed") }},
		{"approval reserved with no receipt", "RSP-P03-approval-reserved", func(m map[string]any) { m["approvalRequestId"] = nil }},
		{"quiescing cleanup contradiction", "RSP-P07-failed-and-cleaned-preserves-failure", func(m map[string]any) { m["launchPhase"] = "quiescing" }},
		{"stopped without cleanup", "RSP-P09-no-workload-quiesce-claim", func(m map[string]any) { m["workloadCleanupConfirmed"] = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var object map[string]any
			if err := json.Unmarshal(responseFixture(t, tc.id), &object); err != nil {
				t.Fatal(err)
			}
			tc.mutate(object["body"].(map[string]any))
			raw, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeUntrustedResponseFrame(append(raw, '\n'))
			if err == nil || !reflect.DeepEqual(got, UntrustedResponse{}) {
				t.Fatal("invalid nested field accepted")
			}
		})
	}
}
