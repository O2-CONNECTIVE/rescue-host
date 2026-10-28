// Package protectedkey は配置済みのホスト鍵を読み取り、新しい鍵を生成しません。
package protectedkey

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"errors"

	"golang.org/x/crypto/ssh"
)

var ErrDenied = errors.New("保護されたホスト鍵を読み取れません")

// KeyProfile は保護された配置元が固定し、wire 要求からは構築しません。
type KeyProfile struct {
	Path, Fingerprint string
	Nodes             []NodePolicy
}

type keySource interface {
	read(context.Context, KeyProfile) ([]byte, error)
}

type Loader struct {
	catalog map[string]KeyProfile
	source  keySource
}

func (l *Loader) Load(ctx context.Context, fixedRef string) (ssh.Signer, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrDenied
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, ErrDenied
	}
	if l == nil || l.source == nil {
		return nil, ErrDenied
	}
	key, ok := l.catalog[fixedRef]
	if !ok {
		return nil, ErrDenied
	}
	data, err := l.source.read(ctx, key)
	if err != nil || ctx.Err() != nil {
		clear(data)
		if err == ErrClosed || err == ErrFatal {
			return nil, err
		}
		return nil, ErrDenied
	}
	defer clear(data) // 最善努力の消去です。Go runtime 内に別の複製が残る可能性があります。
	if len(data) == 0 || len(data) > 16*1024 || !bytes.HasPrefix(data, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n")) {
		return nil, ErrDenied
	}
	// pem.Decode は壊れた先行 block を飛ばすため、探索対象を単一 BEGIN に制限します。
	if bytes.Count(data, []byte("-----BEGIN ")) != 1 {
		return nil, ErrDenied
	}
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, ErrDenied
	}
	defer clear(block.Bytes)
	if block.Type != "OPENSSH PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrDenied
	}
	public, err := outerPublic(block.Bytes)
	if err != nil {
		return nil, ErrDenied
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, ErrDenied
	}
	private, ok := raw.(*ed25519.PrivateKey)
	if !ok || private == nil || len(*private) != ed25519.PrivateKeySize {
		return nil, ErrDenied
	}
	accepted := false
	defer func() {
		if !accepted {
			clear(*private)
		}
	}()
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return nil, ErrDenied
	}
	if ssh.FingerprintSHA256(signer.PublicKey()) != key.Fingerprint {
		return nil, ErrDenied
	}
	if !bytes.Equal(public, signer.PublicKey().Marshal()) {
		return nil, ErrDenied
	}
	message := []byte("rescue-host protected key consistency v1")
	signature, err := signer.Sign(nil, message) // Ed25519 の署名は乱数を使いません。
	if err != nil || signature == nil || signature.Format != ssh.KeyAlgoED25519 || signer.PublicKey().Verify(message, signature) != nil || ctx.Err() != nil {
		return nil, ErrDenied
	}
	accepted = true
	return signer, nil
}
