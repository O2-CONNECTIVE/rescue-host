package bootstrap_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
)

func changedRecord(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	record, _, _ := fixture(t)
	var object map[string]any
	if err := json.Unmarshal(record, &object); err != nil {
		t.Fatal(err)
	}
	change(object)
	out, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// 初期実装への拒否回帰です。後付けの成功を Red→Green と数えません。
func TestDecodeRejectsMalformedFraming(t *testing.T) {
	for name, change := range map[string]func([]byte) []byte{
		"empty":           func([]byte) []byte { return nil },
		"no LF":           func(r []byte) []byte { return r[:len(r)-1] },
		"CRLF":            func(r []byte) []byte { return append(r[:len(r)-1], '\r', '\n') },
		"BOM":             func(r []byte) []byte { return append([]byte{0xef, 0xbb, 0xbf}, r...) },
		"invalid UTF8":    func(r []byte) []byte { r[2] = 0xff; return r },
		"extra object":    func(r []byte) []byte { return append(r, []byte("{}\n")...) },
		"extra LF":        func(r []byte) []byte { return append(r, '\n') },
		"leading space":   func(r []byte) []byte { return append([]byte{' '}, r...) },
		"duplicate field": func(r []byte) []byte { return append([]byte(`{"protocol":"rescue-host-bootstrap/1",`), r[1:]...) },
		"object array":    func(r []byte) []byte { return append(append([]byte{'['}, r[:len(r)-1]...), ']', '\n') },
		"oversize":        func(r []byte) []byte { return append(bytes.Repeat([]byte{' '}, bootstrap.RecordBytes), r...) },
	} {
		t.Run(name, func(t *testing.T) {
			r, e, b := fixture(t)
			if _, err := bootstrap.DecodeRecord(change(r), e, b); err != bootstrap.ErrRecord {
				t.Fatalf("不正frameを形状で拒否しない: %v", err)
			}
		})
	}
}

func TestDecodeRejectsInvalidFieldsAndRepresentations(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"unknown":             func(m map[string]any) { m["approve"] = true },
		"secret field":        func(m map[string]any) { m["privateKey"] = "synthetic-forbidden-field" },
		"case":                func(m map[string]any) { m["Protocol"] = m["protocol"]; delete(m, "protocol") },
		"missing":             func(m map[string]any) { delete(m, "hostKeyRef") },
		"version":             func(m map[string]any) { m["protocol"] = "rescue-host-control/1" },
		"epoch hex":           func(m map[string]any) { m["issuerInstanceEpoch"] = strings.Repeat("A", 64) },
		"arbitrary path":      func(m map[string]any) { m["hostKeyRef"] = `C:\keys\host` },
		"empty ref":           func(m map[string]any) { m["hostKeyRef"] = "" },
		"long ref":            func(m map[string]any) { m["hostKeyRef"] = strings.Repeat("x", 129) },
		"null grant":          func(m map[string]any) { m["grant"] = nil },
		"unknown grant field": func(m map[string]any) { m["grant"].(map[string]any)["renew"] = true },
		"zero generation":     func(m map[string]any) { m["grant"].(map[string]any)["generation"] = "0" },
		"number generation":   func(m map[string]any) { m["grant"].(map[string]any)["generation"] = 1 },
		"generation overflow": func(m map[string]any) { m["grant"].(map[string]any)["generation"] = "18446744073709551616" },
		"leading zero":        func(m map[string]any) { m["grant"].(map[string]any)["generation"] = "01" },
		"sign":                func(m map[string]any) { m["grant"].(map[string]any)["generation"] = "+1" },
		"boundary":            func(m map[string]any) { m["grant"].(map[string]any)["guestBoundary"] = "admin" },
		"non UTC": func(m map[string]any) {
			m["grant"].(map[string]any)["hardDeadline"] = "2026-10-28T12:00:20.000000123+00:00"
		},
		"noncanonical time": func(m map[string]any) {
			m["grant"].(map[string]any)["hardDeadline"] = "2026-10-28T12:00:20.0000001230Z"
		},
		"lease ceiling overflow":    func(m map[string]any) { m["grant"].(map[string]any)["backendLeaseCeilingNs"] = "9223372036854775808" },
		"key digest":                func(m map[string]any) { m["grant"].(map[string]any)["keySetDigest"] = strings.Repeat("d", 64) },
		"duration overflow":         func(m map[string]any) { m["backend"].(map[string]any)["maximumDurationNs"] = "9223372036854775808" },
		"maximum backend ceiling":   func(m map[string]any) { m["backend"].(map[string]any)["maximumDurationNs"] = "7200000000001" },
		"lease above grant ceiling": func(m map[string]any) { m["backend"].(map[string]any)["leaseDurationNs"] = "3000000001" },
		"mode":                      func(m map[string]any) { m["backend"].(map[string]any)["operationMode"] = "unrestricted" },
		"channel count":             func(m map[string]any) { m["backend"].(map[string]any)["maxChannels"] = "33" },
		"handshake count":           func(m map[string]any) { m["backend"].(map[string]any)["maxHandshakes"] = "65" },
		"transport default":         func(m map[string]any) { m["backend"].(map[string]any)["maxTransports"] = "0" },
		"transport count":           func(m map[string]any) { m["backend"].(map[string]any)["maxTransports"] = "65" },
		"no keys":                   func(m map[string]any) { m["backend"].(map[string]any)["allowedKeys"] = []any{} },
		"noncanonical base64": func(m map[string]any) {
			keys := m["backend"].(map[string]any)["allowedKeys"].([]any)
			keys[0] = keys[0].(string) + "="
		},
		"key type": func(m map[string]any) { m["backend"].(map[string]any)["allowedKeys"] = []any{strings.Repeat("A", 68)} },
		"too many keys": func(m map[string]any) {
			b := m["backend"].(map[string]any)
			k := b["allowedKeys"].([]any)[0]
			b["allowedKeys"] = []any{k, k, k, k, k, k, k, k, k}
		},
		"deep containers": func(m map[string]any) {
			var v any = "x"
			for i := 0; i < 9; i++ {
				v = []any{v}
			}
			m["backend"].(map[string]any)["allowedKeys"] = v
		},
		"frame overflow": func(m map[string]any) { m["stdio"].(map[string]any)["frames"] = "2147483648" },
		"cleanup zero":   func(m map[string]any) { m["stdio"].(map[string]any)["cleanupTimeoutNs"] = "0" },
	} {
		t.Run(name, func(t *testing.T) {
			_, e, b := fixture(t)
			if _, err := bootstrap.DecodeRecord(changedRecord(t, change), e, b); err != bootstrap.ErrRecord {
				t.Fatalf("不正fieldを形状/方針で拒否しない: %v", err)
			}
		})
	}
}

func TestDecodeRejectsIndependentExpectedMismatch(t *testing.T) {
	for name, change := range map[string]func(*bootstrap.Expected){
		"issuer":           func(e *bootstrap.Expected) { e.IssuerInstanceEpoch = strings.Repeat("d", 64) },
		"generation":       func(e *bootstrap.Expected) { e.Grant.Binding.Generation++ },
		"identity":         func(e *bootstrap.Expected) { e.Grant.Binding.Identity.LaunchID = "old" },
		"grant key digest": func(e *bootstrap.Expected) { e.Grant.KeySetDigest[0]++ },
		"hard deadline":    func(e *bootstrap.Expected) { e.Grant.HardDeadline = e.Grant.HardDeadline.Add(time.Second) },
		"lease ceiling":    func(e *bootstrap.Expected) { e.Grant.BackendLeaseCeiling++ },
		"artifact":         func(e *bootstrap.Expected) { e.Wire.Binding.ArtifactSHA256 = strings.Repeat("d", 64) },
		"nonce":            func(e *bootstrap.Expected) { e.Wire.Binding.ChannelNonce = strings.Repeat("d", 64) },
		"policy ref":       func(e *bootstrap.Expected) { e.Wire.PolicyRef = "old_policy" },
		"host key ref":     func(e *bootstrap.Expected) { e.HostKeyRef = "old_key" },
		"maximum":          func(e *bootstrap.Expected) { e.Config.MaximumDuration++ },
		"keys":             func(e *bootstrap.Expected) { e.Config.AllowedKeys = nil },
		"frames":           func(e *bootstrap.Expected) { e.Limits.Frames++ },
	} {
		t.Run(name, func(t *testing.T) {
			r, e, b := fixture(t)
			change(&e)
			if _, err := bootstrap.DecodeRecord(r, e, b); err != bootstrap.ErrExpected {
				t.Fatalf("独立した期待束縛の不一致を受理: %v", err)
			}
		})
	}
	r, _, b := fixture(t)
	if _, err := bootstrap.DecodeRecord(r, bootstrap.Expected{}, b); err != bootstrap.ErrExpected {
		t.Fatal("未設定の期待束縛を受理")
	}
}

func TestDecodeEnforcesProtectedReceiverBudgets(t *testing.T) {
	for _, b := range []bootstrap.ReceiverLimits{{}, {MaxFrames: 1, MaxCleanupTimeout: time.Second}, {MaxFrames: 10, MaxCleanupTimeout: time.Nanosecond}} {
		r, e, _ := fixture(t)
		if _, err := bootstrap.DecodeRecord(r, e, b); err != bootstrap.ErrLimits {
			t.Fatalf("受信側の必須予算を超過: %v", err)
		}
	}
}
