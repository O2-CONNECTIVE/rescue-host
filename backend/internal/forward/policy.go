// Package forward は SSH 要求の分類と固定 TCP 宛先への接続を扱う。
package forward

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

var ErrDenied = errors.New("転送要求を拒否しました")

// Policy は両モードとも同じ外側転送制約を持つ。内側 SSH の認可ではない。
type Policy struct{ mode string }

func NewPolicy(mode string) (Policy, error) {
	if mode != "restricted" && mode != "full" {
		return Policy{}, ErrDenied
	}
	return Policy{mode: mode}, nil
}

// Digest は既存 Python 方針と同じ正規化 JSON の SHA-256 である。
func (p Policy) Digest() string {
	data, _ := json.Marshal(map[string]any{
		"schema": 1, "operation_mode": p.mode, "channel": "direct-tcpip",
		"destination":        []any{"127.0.0.1", 22},
		"restricted_helpers": []string{"diagnostics.system", "diagnostics.network", "repair.restart-approved-service"},
	})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type request struct {
	Host       string
	Port       uint32
	Origin     string
	OriginPort uint32
}

// Permit は wire の完全一致だけを見る。名前解決、正規化、trim を行わない。
func (p Policy) Permit(kind string, payload []byte) bool {
	if (p.mode != "restricted" && p.mode != "full") || kind != "direct-tcpip" || len(payload) > 1024 {
		return false
	}
	var value request
	if ssh.Unmarshal(payload, &value) != nil || !bytes.Equal(ssh.Marshal(value), payload) {
		return false
	}
	return value.Host == "127.0.0.1" && value.Port == 22
}

// DialFixed はゲスト由来の文字列も設定値も受け取らない。
// 実 22 番への正例は隔離環境での別検証が必要であり、今回の試験では呼ばない。
func DialFixed(ctx context.Context) (net.Conn, error) {
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", "127.0.0.1:22")
}
