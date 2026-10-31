package auth

import (
	"bytes"
	"crypto/ed25519"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// 公開した固定seedの合成鍵だけを使う。鍵file、実credential、接続は作らない。
func signer(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type metadata struct{ user string }

func (m metadata) User() string        { return m.user }
func (metadata) SessionID() []byte     { return nil }
func (metadata) ClientVersion() []byte { return nil }
func (metadata) ServerVersion() []byte { return nil }
func (metadata) RemoteAddr() net.Addr  { return nil }
func (metadata) LocalAddr() net.Addr   { return nil }

func TestBoundServerConfigAcceptsOnlyTheOuterVerifiedKey(t *testing.T) {
	outer, other, host := signer(t, 1), signer(t, 2), signer(t, 3)
	keys, err := New("launch", []ssh.PublicKey{outer.PublicKey(), other.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	active := true
	config, err := keys.BoundServerConfig(host, func() bool { return active }, outer.PublicKey().Marshal())
	if err != nil {
		t.Fatal(err)
	}
	user := metadata{"launch"}
	if _, err := config.PublicKeyCallback(user, other.PublicKey()); err == nil {
		t.Fatal("同じ集合の別鍵を内側で受理しました")
	}
	if _, err := config.PublicKeyCallback(metadata{"other"}, outer.PublicKey()); err == nil {
		t.Fatal("別usernameを受理しました")
	}
	if _, err := config.VerifiedPublicKeyCallback(user, other.PublicKey(), nil, ssh.KeyAlgoED25519); err == nil {
		t.Fatal("署名検証後に別鍵を受理しました")
	}
	permissions, err := config.VerifiedPublicKeyCallback(user, outer.PublicKey(), nil, ssh.KeyAlgoED25519)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := AuthenticatedKey(permissions)
	if !ok || !bytes.Equal(got, outer.PublicKey().Marshal()) {
		t.Fatal("署名検証済み鍵を記録していません")
	}
	active = false
	if _, err := config.VerifiedPublicKeyCallback(user, outer.PublicKey(), nil, ssh.KeyAlgoED25519); err == nil {
		t.Fatal("失効後に内側認証しました")
	}
}

func TestBoundServerConfigRejectsKeysOutsideTheLaunchSet(t *testing.T) {
	keys, err := New("launch", []ssh.PublicKey{signer(t, 1).PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	for _, bound := range [][]byte{nil, {}, signer(t, 2).PublicKey().Marshal()} {
		if _, err := keys.BoundServerConfig(signer(t, 3), func() bool { return true }, bound); err == nil {
			t.Fatalf("集合外の束縛を受理しました: %x", bound)
		}
	}
}

func TestAuthenticatedKeyRequiresTheServerRecord(t *testing.T) {
	for _, p := range []*ssh.Permissions{nil, {}, {Extensions: map[string]string{authenticatedKey: ""}}, {Extensions: map[string]string{"other": "x"}}, {Extensions: map[string]string{authenticatedKey: "x", "permit-pty": ""}}} {
		if _, ok := AuthenticatedKey(p); ok {
			t.Fatalf("記録のないpermissionsを認証済み鍵にしました: %+v", p)
		}
	}
}
