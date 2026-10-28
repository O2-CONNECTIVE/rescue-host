package protectedkey

import (
	"bytes"
	"crypto/ed25519"

	"golang.org/x/crypto/ssh"
)

const openSSHMagic = "openssh-key-v1\x00"

// ParseRawPrivateKey が照合しない外側の公開鍵も検査します。
func outerPublic(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(openSSHMagic)) {
		return nil, ErrDenied
	}
	var outer struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      []byte
	}
	if err := ssh.Unmarshal(data[len(openSSHMagic):], &outer); err != nil || outer.Cipher != "none" || outer.KDF != "none" || outer.Options != "" || outer.Count != 1 {
		return nil, ErrDenied
	}
	public, err := ssh.ParsePublicKey(outer.Public)
	if err != nil || public.Type() != ssh.KeyAlgoED25519 {
		return nil, ErrDenied
	}
	var inner struct {
		Check1, Check2  uint32
		Algorithm       string
		Public, Private []byte
		Comment         string
		Padding         []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(outer.Private, &inner); err != nil || inner.Check1 != inner.Check2 || inner.Algorithm != ssh.KeyAlgoED25519 || len(inner.Public) != ed25519.PublicKeySize || len(inner.Private) != ed25519.PrivateKeySize {
		return nil, ErrDenied
	}
	if !bytes.Equal(inner.Public, inner.Private[ed25519.SeedSize:]) {
		return nil, ErrDenied
	}
	innerPublic, err := ssh.NewPublicKey(ed25519.PublicKey(inner.Public))
	if err != nil || !bytes.Equal(outer.Public, innerPublic.Marshal()) {
		return nil, ErrDenied
	}
	return outer.Public, nil
}
