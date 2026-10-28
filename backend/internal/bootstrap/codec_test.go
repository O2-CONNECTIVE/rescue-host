package bootstrap_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

// RFC 8032 の公開 test vector だけをメモリ内で使う。
func fixture(t *testing.T) ([]byte, bootstrap.Expected, bootstrap.ReceiverLimits) {
	t.Helper()
	public, err := hex.DecodeString("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	if err != nil {
		t.Fatal(err)
	}
	marshal := append([]byte{0, 0, 0, 11}, []byte("ssh-ed25519")...)
	marshal = append(marshal, 0, 0, 0, 32)
	marshal = append(marshal, public...)
	key, err := ssh.ParsePublicKey(marshal)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{key})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := backend.PolicyDigest("restricted")
	if err != nil {
		t.Fatal(err)
	}
	id := backend.Identity{LaunchID: "launch_1", Epoch: "0", PolicyDigest: policy}
	wire := control.WireContext{Generation: "1", Identity: control.WireIdentity{LaunchID: id.LaunchID, Epoch: id.Epoch, PolicyDigest: policy}, Binding: control.Binding{ArtifactSHA256: strings.Repeat("a", 64), ChannelNonce: strings.Repeat("b", 64)}, PolicyRef: "policy_1"}
	grant := supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: id}, KeySetDigest: digest, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: time.Date(2026, 10, 28, 12, 0, 20, 123, time.UTC), BackendLeaseCeiling: 3 * time.Second}
	encoded := base64.StdEncoding.EncodeToString(marshal)
	expected := bootstrap.Expected{IssuerInstanceEpoch: strings.Repeat("c", 64), Grant: grant, Wire: wire, HostKeyRef: "host_key_1", Config: bootstrap.Config{OperationMode: "restricted", AllowedKeys: []string{encoded}, MaximumDuration: 20 * time.Second, LeaseDuration: time.Second, MaxChannels: 2, MaxHandshakes: 3, MaxTransports: 4}, Limits: stdio.Limits{FrameBytes: 16384, Frames: 10, CleanupTimeout: time.Second}}
	record := fmt.Sprintf(`{"protocol":"rescue-host-bootstrap/1","issuerInstanceEpoch":"%s","grant":{"generation":"1","identity":{"launchId":"launch_1","epoch":"0","policyDigest":"%s"},"keySetDigest":"%x","guestBoundary":"separated_non_admin_guest","hardDeadline":"2026-10-28T12:00:20.000000123Z","backendLeaseCeilingNs":"3000000000"},"binding":{"artifactSha256":"%s","channelNonce":"%s"},"policyRef":"policy_1","backend":{"operationMode":"restricted","allowedKeys":["%s"],"maximumDurationNs":"20000000000","leaseDurationNs":"1000000000","maxChannels":"2","maxHandshakes":"3","maxTransports":"4"},"hostKeyRef":"host_key_1","stdio":{"frames":"10","cleanupTimeoutNs":"1000000000"}}`+"\n", expected.IssuerInstanceEpoch, policy, digest, wire.Binding.ArtifactSHA256, wire.Binding.ChannelNonce, encoded)
	return []byte(record), expected, bootstrap.ReceiverLimits{MaxFrames: 100, MaxCleanupTimeout: 2 * time.Second}
}

func TestEncodeMatchesIndependentCanonicalLiteral(t *testing.T) {
	record, expected, limits := fixture(t)
	message, err := bootstrap.DecodeRecord(record, expected, limits)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := bootstrap.EncodeRecord(message)
	if err != nil || !bytes.Equal(encoded, record) {
		t.Fatalf("独立literalと正規表現が異なる: %v", err)
	}
	expected.Config.AllowedKeys[0] = "caller-mutated"
	clear(record)
	if message.Config.AllowedKeys[0] == "caller-mutated" || !bytes.Contains(encoded, []byte(`"protocol":"rescue-host-bootstrap/1"`)) {
		t.Fatal("callerの入力変更が複製した結果へ伝播した")
	}
}

func TestEncodeRejectsInvalidTypedValues(t *testing.T) {
	for name, change := range map[string]func(*bootstrap.Message){
		"unbound identity":   func(m *bootstrap.Message) { m.Grant.Binding.Identity.LaunchID = "other" },
		"unbound generation": func(m *bootstrap.Message) { m.Wire.Generation = "2" },
		"missing issuer":     func(m *bootstrap.Message) { m.IssuerInstanceEpoch = "" },
		"wrong frame bytes":  func(m *bootstrap.Message) { m.Limits.FrameBytes = 1 },
		"zero deadline":      func(m *bootstrap.Message) { m.Grant.HardDeadline = time.Time{} },
		"duration ceiling":   func(m *bootstrap.Message) { m.Config.MaximumDuration = 2*time.Hour + 1 },
		"lease ceiling":      func(m *bootstrap.Message) { m.Config.LeaseDuration = m.Grant.BackendLeaseCeiling + 1 },
		"huge key input": func(m *bootstrap.Message) {
			m.Config.AllowedKeys = []string{strings.Repeat("x", bootstrap.RecordBytes+1)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, e, b := fixture(t)
			m, err := bootstrap.DecodeRecord(r, e, b)
			if err != nil {
				t.Fatal(err)
			}
			change(&m)
			if out, err := bootstrap.EncodeRecord(m); out != nil || err != bootstrap.ErrRecord {
				t.Fatalf("不正な内部値をserialize: %v", err)
			}
		})
	}
}

func TestDecodeLiteralRecord(t *testing.T) {
	record, expected, limits := fixture(t)
	got, err := bootstrap.DecodeRecord(record, expected, limits)
	if err != nil {
		t.Fatalf("既知の公開鍵と固定絶対期限の記録を拒否: %v", err)
	}
	if got.Grant != expected.Grant || got.Wire != expected.Wire || got.HostKeyRef != "host_key_1" || got.Config.MaxTransports != 4 || got.Limits.Frames != 10 {
		t.Fatalf("束縛値が失われた: %#v", got)
	}
}

func TestRejectTrailingWhitespace(t *testing.T) {
	record, expected, limits := fixture(t)
	record = append(record[:len(record)-1], ' ', '\n')
	if _, err := bootstrap.DecodeRecord(record, expected, limits); err == nil {
		t.Fatal("object と LF の間の余剰 byte を受理した")
	}
}
