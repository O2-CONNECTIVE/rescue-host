package protectedkey

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const testRef = "provisioned-host-ed25519"
const testFingerprint = "SHA256:bbXpuKG6zhzdmnxq256TlqzFBzRl2f6OOg722cYNbU8"

type memorySource struct {
	data  []byte
	calls int
}

func (m *memorySource) read(context.Context, KeyProfile) ([]byte, error) {
	m.calls++
	return append([]byte(nil), m.data...), nil
}

// RFC 8032 TEST 1 の公開 test vector はメモリだけで扱い、保存しません。
func syntheticPEM(t *testing.T) []byte {
	t.Helper()
	seed, err := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	inner := ssh.Marshal(struct {
		Check1, Check2  uint32
		Algorithm       string
		Public, Private []byte
		Comment         string
	}{0x13572468, 0x13572468, ssh.KeyAlgoED25519, key[32:], key, "public synthetic test"})
	for i := byte(1); len(inner)%8 != 0; i++ {
		inner = append(inner, i)
	}
	outer := ssh.Marshal(struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      []byte
	}{"none", "none", "", 1, pub.Marshal(), inner})
	return pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: append([]byte("openssh-key-v1\x00"), outer...)})
}

func testLoader(data []byte) (*Loader, *memorySource) {
	source := &memorySource{data: data}
	return &Loader{catalog: map[string]KeyProfile{testRef: {Path: `C:\protected\host-key`, Fingerprint: testFingerprint}}, source: source}, source
}

func loadContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLoadExistingSyntheticOpenSSHEd25519(t *testing.T) {
	loader, source := testLoader(syntheticPEM(t))
	signer, err := loader.Load(loadContext(t), testRef)
	if err != nil || signer == nil {
		t.Fatalf("Load = %v, %v", signer, err)
	}
	if got := ssh.FingerprintSHA256(signer.PublicKey()); got != testFingerprint {
		t.Fatalf("fingerprint = %s", got)
	}
	message := []byte("protected-key loader public boundary")
	sig, err := signer.Sign(rand.Reader, message)
	if err != nil || signer.PublicKey().Verify(message, sig) != nil {
		t.Fatal("loaded signer does not verify")
	}
	if source.calls != 1 {
		t.Fatalf("read calls = %d", source.calls)
	}
}

func TestLoadRejectsUnregisteredReferenceBeforeReading(t *testing.T) {
	for _, ref := range []string{"", "unknown", `C:\protected\host-key`, "../host-key"} {
		loader, source := testLoader(syntheticPEM(t))
		signer, err := loader.Load(loadContext(t), ref)
		if signer != nil || err != ErrDenied || source.calls != 0 {
			t.Fatalf("unregistered reference accepted or read")
		}
	}
}

func TestLoadRequiresLiveDeadlineBeforeReading(t *testing.T) {
	cancelled, cancel := context.WithCancel(loadContext(t))
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	for _, ctx := range []context.Context{nil, context.Background(), cancelled, expired} {
		loader, source := testLoader(syntheticPEM(t))
		signer, err := loader.Load(ctx, testRef)
		if signer != nil || err != ErrDenied || source.calls != 0 {
			t.Fatal("invalid context accepted or read")
		}
	}
}

func TestLoadRejectsAdditionalPEMOrTextAndOversize(t *testing.T) {
	valid := syntheticPEM(t)
	for name, data := range map[string][]byte{
		"prefix":       append([]byte("ignored text\n"), valid...),
		"suffix":       append(append([]byte(nil), valid...), []byte("ignored text\n")...),
		"second block": append(append([]byte(nil), valid...), valid...),
		"over 16KiB":   append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), 16385-len(valid))...),
	} {
		t.Run(name, func(t *testing.T) {
			loader, _ := testLoader(data)
			signer, err := loader.Load(loadContext(t), testRef)
			if signer != nil || err != ErrDenied {
				t.Fatal("noncanonical or oversized key accepted")
			}
		})
	}
}

func TestLoadRejectsFingerprintMismatch(t *testing.T) {
	loader, _ := testLoader(syntheticPEM(t))
	entry := loader.catalog[testRef]
	entry.Fingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	loader.catalog[testRef] = entry
	signer, err := loader.Load(loadContext(t), testRef)
	if signer != nil || err != ErrDenied {
		t.Fatal("unprovisioned public fingerprint accepted")
	}
}

func TestLoadRejectsOuterPublicMismatch(t *testing.T) {
	block, _ := pem.Decode(syntheticPEM(t))
	var outer struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      []byte
	}
	if err := ssh.Unmarshal(block.Bytes[len("openssh-key-v1\x00"):], &outer); err != nil {
		t.Fatal(err)
	}
	outer.Public[len(outer.Public)-1] ^= 1
	data := pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: append([]byte("openssh-key-v1\x00"), ssh.Marshal(outer)...)})
	loader, _ := testLoader(data)
	signer, err := loader.Load(loadContext(t), testRef)
	if signer != nil || err != ErrDenied {
		t.Fatal("unbound outer public key accepted")
	}
}

func TestLoadRejectsSeedPublicMismatch(t *testing.T) {
	block, _ := pem.Decode(syntheticPEM(t))
	var outer struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      []byte
	}
	if err := ssh.Unmarshal(block.Bytes[len("openssh-key-v1\x00"):], &outer); err != nil {
		t.Fatal(err)
	}
	var inner struct {
		Check1, Check2  uint32
		Algorithm       string
		Public, Private []byte
		Comment         string
		Padding         []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(outer.Private, &inner); err != nil {
		t.Fatal(err)
	}
	inner.Private[0] ^= 1 // Keep all advertised public bytes and fingerprint unchanged.
	outer.Private = ssh.Marshal(inner)
	data := pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: append([]byte("openssh-key-v1\x00"), ssh.Marshal(outer)...)})
	loader, _ := testLoader(data)
	signer, err := loader.Load(loadContext(t), testRef)
	if signer != nil || err != ErrDenied {
		t.Fatal("inconsistent seed and public key accepted")
	}
}

func TestLoadRejectsInnerPublicMismatch(t *testing.T) {
	block, _ := pem.Decode(syntheticPEM(t))
	var outer struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      []byte
	}
	if err := ssh.Unmarshal(block.Bytes[len("openssh-key-v1\x00"):], &outer); err != nil {
		t.Fatal(err)
	}
	var inner struct {
		Check1, Check2  uint32
		Algorithm       string
		Public, Private []byte
		Comment         string
		Padding         []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(outer.Private, &inner); err != nil {
		t.Fatal(err)
	}
	inner.Public[0] ^= 1
	outer.Private = ssh.Marshal(inner)
	data := pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: append([]byte("openssh-key-v1\x00"), ssh.Marshal(outer)...)})
	loader, _ := testLoader(data)
	signer, err := loader.Load(loadContext(t), testRef)
	if signer != nil || err != ErrDenied {
		t.Fatal("inconsistent inner public key accepted")
	}
}
