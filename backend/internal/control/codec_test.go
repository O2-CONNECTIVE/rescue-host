package control

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

type sharedVectors struct {
	WireCases []struct {
		ID            string `json:"id"`
		FrameAccepted bool   `json:"frameAccepted"`
		FrameParts    []struct {
			Hex           string `json:"hex"`
			RepeatByteHex string `json:"repeatByteHex"`
			Count         int    `json:"count"`
		} `json:"frameParts"`
	} `json:"wireCases"`
}

func vectors(t *testing.T) sharedVectors {
	t.Helper()
	data, err := os.ReadFile("../../../contracts/control-v1/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v sharedVectors
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.WireCases) != 49 {
		t.Fatal("shared vector count changed; review contract head")
	}
	return v
}
func TestSharedHelloCodec(t *testing.T) {
	v := vectors(t)
	frame, err := hex.DecodeString(v.WireCases[0].FrameParts[0].Hex)
	if err != nil {
		t.Fatal(err)
	}
	message, err := Decode(frame)
	if err != nil || message.Kind != "hello" || message.Identity.Epoch != "1" {
		t.Fatal("shared hello rejected")
	}
}

// 各byte列を正本から直接読み、実装から期待値を生成しない。
func TestSharedWireVectors(t *testing.T) {
	for _, test := range vectors(t).WireCases {
		t.Run(test.ID, func(t *testing.T) {
			var frame []byte
			for _, part := range test.FrameParts {
				if part.Hex != "" {
					b, err := hex.DecodeString(part.Hex)
					if err != nil {
						t.Fatal(err)
					}
					frame = append(frame, b...)
				} else {
					b, err := hex.DecodeString(part.RepeatByteHex)
					if err != nil || len(b) != 1 || part.Count < 0 || part.Count > 20000 {
						t.Fatal("invalid finite vector")
					}
					frame = append(frame, bytes.Repeat(b, part.Count)...)
				}
			}
			message, err := Decode(frame)
			if (err == nil) != test.FrameAccepted {
				t.Fatalf("acceptance differs from shared vector: accepted=%v", err == nil)
			}
			if err == nil {
				encoded, err := Encode(message)
				if err != nil {
					t.Fatal("accepted frame could not encode")
				}
				if _, err = Decode(encoded); err != nil {
					t.Fatal("encoded frame rejected")
				}
			}
		})
	}
}

func TestSharedContractProvenance(t *testing.T) {
	for name, want := range map[string]string{"schema.json": "1191b215741dd07b6475bdb55ef46da1e91b38365562092d97dad0e8d97c42b2", "vectors.json": "ce687710dbf3daee7fab32d341b1255d90f17072aae8bc8d5d1af3bed5b856f0"} {
		data, err := os.ReadFile("../../../contracts/control-v1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		// JSON文書のcheckout改行だけを吸収する。wire byte列はhexのまま不変。
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		if fmt.Sprintf("%x", sha256.Sum256(data)) != want {
			t.Fatal("shared contract differs from ef556a26; review owner handoff")
		}
	}
}

func TestEncodeRejectsOversizedInternalValues(t *testing.T) {
	_, err := Encode(Message{Protocol: Protocol, Payload: bytes.Repeat([]byte(" "), FrameBytes+1)})
	if err != InvalidMessage {
		t.Fatal("oversized internal payload accepted")
	}
}
