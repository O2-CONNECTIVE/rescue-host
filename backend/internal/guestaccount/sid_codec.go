package guestaccount

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

const sidFrameLimit = 16384

// この内部形式はcontrol/1ではない。全fieldは既に保護側で確定した相関値。
// requestのbyte列を署名/認証proofとして使わない。
type sidFrame struct {
	Protocol   string `json:"protocol"`
	Operation  string `json:"operation"`
	Epoch      string `json:"epoch"`
	Intent     string `json:"intent"`
	Policy     string `json:"policy"`
	Generation string `json:"generation"`
	Machine    string `json:"machine"`
	Operator   string `json:"operator"`
	Service    string `json:"service"`
	Name       string `json:"name"`
	SID        string `json:"sid"`
}

func sidRequest(b binding, op accountKind) []byte {
	operation := ""
	if op == disabled {
		operation = "disable"
	}
	if op == deleted {
		operation = "delete"
	}
	if operation == "" {
		return nil
	}
	return encodeSIDFrame(sidFrameFor(b, operation))
}
func sidFrameFor(b binding, operation string) sidFrame {
	return sidFrame{"rescue-host-guest-sid/1", operation, hex.EncodeToString(b.epoch[:]), hex.EncodeToString(b.intent[:]), hex.EncodeToString(b.policy[:]), strconv.FormatUint(b.generation, 10), b.machine, b.operator, b.service, b.name, b.sid}
}
func encodeSIDFrame(w sidFrame) []byte {
	encoded, err := json.Marshal(w)
	if err != nil || len(encoded)+1 > sidFrameLimit {
		return nil
	}
	return append(encoded, '\n')
}

// scriptは自分が完了した要求を固定envelopeで返す。canonical byte比較により
// duplicate/unknown field、型変化、別要求、余分なframeをすべて拒否する。
// 実EOF/child終了/Job回収の証拠はexecutorが別に確認する。
func sidResult(request []byte) []byte {
	if len(request) == 0 || request[len(request)-1] != '\n' {
		return nil
	}
	out := append([]byte(`{"protocol":"rescue-host-guest-result/1","request":`), request[:len(request)-1]...)
	return append(out, []byte(",\"outcome\":\"ok\"}\n")...)
}
func validSIDResult(request, result []byte) bool {
	return len(request) != 0 && len(result) <= sidFrameLimit && bytes.Equal(sidResult(request), result)
}
