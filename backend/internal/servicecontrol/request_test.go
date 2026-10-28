package servicecontrol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type requestVector struct {
	ID                        string `json:"id"`
	Bytes                     int    `json:"bytes"`
	RawBase64                 string `json:"rawBase64"`
	StructuralAccepted        bool   `json:"structuralAccepted"`
	AuthenticationEstablished bool   `json:"authenticationEstablished"`
}
type vectorFile struct {
	Protocol           string          `json:"protocol"`
	Cases              []requestVector `json:"cases"`
	ParserDepthVectors struct {
		Cases []struct {
			ID                        string `json:"id"`
			Bytes                     int    `json:"bytes"`
			RawBase64                 string `json:"rawBase64"`
			DepthParserAccepted       bool   `json:"depthParserAccepted"`
			ProtocolSchemaAccepted    bool   `json:"protocolSchemaAccepted"`
			AuthenticationEstablished bool   `json:"authenticationEstablished"`
		} `json:"cases"`
	} `json:"parserDepthVectors"`
}

func vectors(t *testing.T) vectorFile {
	t.Helper()
	data, err := os.ReadFile("testdata/request-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if v.Protocol != "rescue-host-service/1" {
		t.Fatal("wrong vector protocol")
	}
	return v
}
func vectorBytes(t *testing.T, raw string, size int) []byte {
	t.Helper()
	frame, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || len(frame) != size {
		t.Fatal("invalid fixed vector", err)
	}
	return frame
}
func TestDecodeUntrustedHelloVector(t *testing.T) {
	v := vectors(t).Cases[0]
	if v.ID != "hello" || !v.StructuralAccepted || v.AuthenticationEstablished {
		t.Fatal("unexpected hello fixture")
	}
	got, err := DecodeUntrustedFrame(vectorBytes(t, v.RawBase64, v.Bytes))
	want := UntrustedRequest{Type: Hello, RequestID: strings.Repeat("1", 32), ClientNonce: strings.Repeat("2", 64)}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("hello structural decode: got=%+v err=%v", got, err)
	}
}

func TestDecodeUntrustedPositiveRequestVectors(t *testing.T) {
	count := 1 // helloは独立した最初のTDD sliceで検査する。
	for _, v := range vectors(t).Cases {
		if !v.StructuralAccepted || v.ID == "hello" {
			continue
		}
		count++
		t.Run(v.ID, func(t *testing.T) {
			if v.AuthenticationEstablished {
				t.Fatal("a structural fixture cannot establish authentication")
			}
			want := UntrustedRequest{Type: Request, Sequence: 1, RequestID: strings.Repeat("c", 32), BootEpoch: strings.Repeat("a", 64), ChannelNonce: strings.Repeat("b", 64), Command: Status}
			switch v.ID {
			case "prepare-intent":
				want.Command = PrepareIntent
			case "confirm-approval":
				want.Command = ConfirmApproval
				want.Body = ConfirmationFields{IntentID: strings.Repeat("d", 32), PreviewDigest: strings.Repeat("e", 64)}
			case "revoke-all-and-quiesce":
				want.Command = RevokeAllAndQuiesce
			case "uint64-max-shape-only-state-will-reject-quota":
				want.Sequence = 18446744073709551615
			case "status", "frame-limit-equal":
			default:
				t.Fatal("unrecognized positive vector")
			}
			got, err := DecodeUntrustedFrame(vectorBytes(t, v.RawBase64, v.Bytes))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("untrusted structural request: got=%+v err=%v", got, err)
			}
		})
	}
	if count != 7 {
		t.Fatalf("positive vector inventory=%d", count)
	}
}

func TestDecodeUntrustedNegativeVectors(t *testing.T) {
	count := 0
	for _, v := range vectors(t).Cases {
		if v.StructuralAccepted {
			continue
		}
		count++
		t.Run(v.ID, func(t *testing.T) {
			if v.AuthenticationEstablished {
				t.Fatal("unexpected authenticated fixture")
			}
			got, err := DecodeUntrustedFrame(vectorBytes(t, v.RawBase64, v.Bytes))
			if err == nil || got != (UntrustedRequest{}) {
				t.Fatal("invalid request accepted or partial value escaped")
			}
		})
	}
	if count != 21 {
		t.Fatalf("negative vector inventory=%d", count)
	}
}

func TestDecodeUntrustedParserDepthVectors(t *testing.T) {
	cases := vectors(t).ParserDepthVectors.Cases
	if len(cases) != 2 {
		t.Fatal("depth vector inventory")
	}
	for _, v := range cases {
		t.Run(v.ID, func(t *testing.T) {
			if v.ProtocolSchemaAccepted || v.AuthenticationEstablished {
				t.Fatal("generic depth cases are not service requests")
			}
			got, err := DecodeUntrustedFrame(vectorBytes(t, v.RawBase64, v.Bytes))
			want := ErrDepth
			if v.DepthParserAccepted {
				want = ErrSchema
			}
			if got != (UntrustedRequest{}) || !errors.Is(err, want) {
				t.Fatalf("depth phase: got=%+v err=%v want=%v", got, err, want)
			}
		})
	}
}

func TestContainerDepthLeavesScalarAndKeysAtTheSameDepth(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      error
	}{
		{"array depth8 empty", `{"x":` + strings.Repeat("[", 7) + strings.Repeat("]", 7) + "}\n", ErrSchema},
		{"array depth8 scalar", `{"x":` + strings.Repeat("[", 7) + "0" + strings.Repeat("]", 7) + "}\n", ErrSchema},
		{"array depth9", `{"x":` + strings.Repeat("[", 8) + strings.Repeat("]", 8) + "}\n", ErrDepth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeUntrustedFrame([]byte(tc.raw))
			if got != (UntrustedRequest{}) || !errors.Is(err, tc.want) {
				t.Fatalf("container depth: %v", err)
			}
		})
	}
}

func TestCorrelationShapesAndExactCommandBodies(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		mutate     func(map[string]any)
	}{
		{"hello request sequence", "hello", func(m map[string]any) { m["sequence"] = "1" }},
		{"hello numeric zero", "hello", func(m map[string]any) { m["sequence"] = 0 }},
		{"hello client nonce length", "hello", func(m map[string]any) { m["clientNonce"] = strings.Repeat("a", 63) }},
		{"hello extra boot binding", "hello", func(m map[string]any) { m["bootEpoch"] = strings.Repeat("a", 64) }},
		{"request id length", "status", func(m map[string]any) { m["requestId"] = strings.Repeat("a", 31) }},
		{"boot epoch uppercase", "status", func(m map[string]any) { m["bootEpoch"] = strings.Repeat("A", 64) }},
		{"boot epoch type", "status", func(m map[string]any) { m["bootEpoch"] = 1 }},
		{"channel nonce nonhex", "status", func(m map[string]any) { m["channelNonce"] = strings.Repeat("g", 64) }},
		{"channel nonce type", "status", func(m map[string]any) { m["channelNonce"] = nil }},
		{"client nonce on request", "status", func(m map[string]any) { m["clientNonce"] = strings.Repeat("a", 64) }},
		{"status body fields", "status", func(m map[string]any) { m["body"] = map[string]any{"intentId": strings.Repeat("d", 32)} }},
		{"prepare body fields", "prepare-intent", func(m map[string]any) { m["body"] = map[string]any{"approved": true} }},
		{"revoke body fields", "revoke-all-and-quiesce", func(m map[string]any) { m["body"] = map[string]any{"force": true} }},
		{"intent id length", "confirm-approval", func(m map[string]any) { m["body"].(map[string]any)["intentId"] = strings.Repeat("d", 31) }},
		{"intent id type", "confirm-approval", func(m map[string]any) { m["body"].(map[string]any)["intentId"] = true }},
		{"preview digest uppercase", "confirm-approval", func(m map[string]any) { m["body"].(map[string]any)["previewDigest"] = strings.Repeat("E", 64) }},
		{"preview digest absent", "confirm-approval", func(m map[string]any) { delete(m["body"].(map[string]any), "previewDigest") }},
		{"confirmation extra field", "confirm-approval", func(m map[string]any) { m["body"].(map[string]any)["approved"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var object map[string]any
			found := false
			for _, v := range vectors(t).Cases {
				if v.ID == tc.base {
					if err := json.Unmarshal(vectorBytes(t, v.RawBase64, v.Bytes), &object); err != nil {
						t.Fatal(err)
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatal("missing base vector")
			}
			tc.mutate(object)
			frame, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeUntrustedFrame(append(frame, '\n'))
			if err == nil || got != (UntrustedRequest{}) {
				t.Fatal("invalid correlation or body accepted")
			}
		})
	}
}

func TestNonfiniteAndUntrustedJSONValuesAreRejected(t *testing.T) {
	for _, raw := range []string{"", "\n", "null\n", "[]\n", "NaN\n", "Infinity\n", "{\"x\":NaN}\n", "{\"x\":-Infinity}\n", "{\"x\":1e9999}\n", "{\"x\":true}\n"} {
		got, err := DecodeUntrustedFrame([]byte(raw))
		if err == nil || got != (UntrustedRequest{}) {
			t.Fatal("untrusted non-request JSON accepted")
		}
	}
}

func TestDecodedValuesSurviveBufferReuseWithoutChannelState(t *testing.T) {
	var v requestVector
	for _, candidate := range vectors(t).Cases {
		if candidate.ID == "status" {
			v = candidate
			break
		}
	}
	frame := vectorBytes(t, v.RawBase64, v.Bytes)
	first, err := DecodeUntrustedFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	clear(frame)
	if first.RequestID != strings.Repeat("c", 32) || first.BootEpoch != strings.Repeat("a", 64) || first.ChannelNonce != strings.Repeat("b", 64) || first.Sequence != 1 || first.Command != Status {
		t.Fatal("decoded values alias reused read buffer")
	}
	second, err := DecodeUntrustedFrame(vectorBytes(t, v.RawBase64, v.Bytes))
	if err != nil || second != first {
		t.Fatal("pure decoder unexpectedly retained sequence/ID channel state")
	}
}
