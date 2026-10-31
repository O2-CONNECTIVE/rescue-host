// Package auth は起動時に固定した鍵集合を SSH ライブラリの署名検証へ渡す。
package auth

import (
	"bytes"
	"errors"

	"golang.org/x/crypto/ssh"
)

var ErrDenied = errors.New("SSH 認証を拒否しました")

// Keys は入力のスライスや鍵オブジェクトを保持しない、不変の公開鍵集合である。
type Keys struct {
	username string
	allowed  map[string]struct{}
}

// New は raw Ed25519 のみを受け付ける。証明書や暗黙の追加認証源は使わない。
func New(username string, publicKeys []ssh.PublicKey) (*Keys, error) {
	if username == "" || len(username) > 128 || len(publicKeys) == 0 || len(publicKeys) > 8 {
		return nil, ErrDenied
	}
	k := &Keys{username: username, allowed: make(map[string]struct{})}
	for _, input := range publicKeys {
		if input == nil || input.Type() != ssh.KeyAlgoED25519 {
			return nil, ErrDenied
		}
		parsed, err := ssh.ParsePublicKey(input.Marshal())
		if err != nil || parsed.Type() != ssh.KeyAlgoED25519 {
			return nil, ErrDenied
		}
		k.allowed[string(parsed.Marshal())] = struct{}{}
	}
	return k, nil
}

// 署名検証済みの鍵だけをこの拡張名で記録する。clientが送る値ではない。
const authenticatedKey = "rescue-host-authenticated-key"

// AuthenticatedKey は同じ ServerConfig が署名検証後に記録した raw 鍵を返す。
// 記録が無い接続を認証済み鍵として扱わない。
func AuthenticatedKey(p *ssh.Permissions) ([]byte, bool) {
	if p == nil || len(p.Extensions) != 1 {
		return nil, false
	}
	value, ok := p.Extensions[authenticatedKey]
	if !ok || value == "" {
		return nil, false
	}
	return []byte(value), true
}

// ServerConfig は鍵照会と署名検証後の両方で有効性を確認する。
// 秘密鍵所持の検証そのものは固定版の SSH ライブラリに委ねる。
func (k *Keys) ServerConfig(host ssh.Signer, active func() bool) (*ssh.ServerConfig, error) {
	return k.serverConfig(host, active, nil)
}

// BoundServerConfig は同じ鍵集合のうち、外側で署名検証済みの1鍵だけを許す。
// 集合外の値や空値を束縛に使わず、別鍵での内側認証を拒否する。
func (k *Keys) BoundServerConfig(host ssh.Signer, active func() bool, bound []byte) (*ssh.ServerConfig, error) {
	if _, ok := k.allowed[string(bound)]; !ok || len(bound) == 0 {
		return nil, ErrDenied
	}
	return k.serverConfig(host, active, bytes.Clone(bound))
}

func (k *Keys) serverConfig(host ssh.Signer, active func() bool, bound []byte) (*ssh.ServerConfig, error) {
	if host == nil || host.PublicKey().Type() != ssh.KeyAlgoED25519 || active == nil {
		return nil, ErrDenied
	}
	permit := func(c ssh.ConnMetadata, key ssh.PublicKey) bool {
		if !active() || c.User() != k.username || key.Type() != ssh.KeyAlgoED25519 {
			return false
		}
		data := key.Marshal()
		if bound != nil && !bytes.Equal(data, bound) {
			return false
		}
		_, ok := k.allowed[string(data)]
		return ok
	}
	config := &ssh.ServerConfig{
		NoClientAuth:            false,
		MaxAuthTries:            3,
		PublicKeyAuthAlgorithms: []string{ssh.KeyAlgoED25519},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !permit(c, key) {
				return nil, ErrDenied
			}
			return &ssh.Permissions{}, nil
		},
		VerifiedPublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey, permissions *ssh.Permissions, algorithm string) (*ssh.Permissions, error) {
			if algorithm != ssh.KeyAlgoED25519 || !permit(c, key) {
				return nil, ErrDenied
			}
			return &ssh.Permissions{Extensions: map[string]string{authenticatedKey: string(key.Marshal())}}, nil
		},
	}
	config.AddHostKey(host)
	return config, nil
}
