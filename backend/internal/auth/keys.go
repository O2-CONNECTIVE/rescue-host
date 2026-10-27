// Package auth は起動時に固定した鍵集合を SSH ライブラリの署名検証へ渡す。
package auth

import (
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

// ServerConfig は鍵照会と署名検証後の両方で有効性を確認する。
// 秘密鍵所持の検証そのものは固定版の SSH ライブラリに委ねる。
func (k *Keys) ServerConfig(host ssh.Signer, active func() bool) (*ssh.ServerConfig, error) {
	if host == nil || host.PublicKey().Type() != ssh.KeyAlgoED25519 || active == nil {
		return nil, ErrDenied
	}
	permit := func(c ssh.ConnMetadata, key ssh.PublicKey) bool {
		if !active() || c.User() != k.username || key.Type() != ssh.KeyAlgoED25519 {
			return false
		}
		_, ok := k.allowed[string(key.Marshal())]
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
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(host)
	return config, nil
}
