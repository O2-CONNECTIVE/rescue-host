package backend

import (
	"context"
	"crypto/rand"
	"golang.org/x/crypto/ssh"
	"net"
	"sync/atomic"
	"testing"
)

// 設定入力のcertificate拒否とは別に、実SSH上の証明書candidateを観測する。
func TestCloseoutN11WireCertificateDenied(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			cfg, signer := config(t, mode)
			var dials, candidates, verified atomic.Int32
			s, e := newSession(cfg, func(context.Context) (net.Conn, error) {
				dials.Add(1)
				t.Error("認証拒否試験でtarget dial")
				return nil, ErrNotAccepted
			})
			if e != nil {
				t.Fatal(e)
			}
			base := s.config.PublicKeyCallback
			s.config.PublicKeyCallback = func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
				if _, ok := k.(*ssh.Certificate); ok {
					candidates.Add(1)
				}
				return base(c, k)
			}
			baseVerified := s.config.VerifiedPublicKeyCallback
			s.config.VerifiedPublicKeyCallback = func(c ssh.ConnMetadata, k ssh.PublicKey, p *ssh.Permissions, a string) (*ssh.Permissions, error) {
				verified.Add(1)
				return baseVerified(c, k, p, a)
			}
			cert := &ssh.Certificate{Key: signer.PublicKey(), Serial: 1, CertType: ssh.UserCert, KeyId: "memory-only-fixture", ValidPrincipals: []string{cfg.Identity.LaunchID}, ValidBefore: ssh.CertTimeInfinity}
			if e = cert.SignCert(rand.Reader, key(t)); e != nil {
				t.Fatal(e)
			}
			certificateSigner, e := ssh.NewCertSigner(cert, signer)
			if e != nil {
				t.Fatal(e)
			}
			address := start(t, s, cfg)
			cl, e := client(address, cfg, certificateSigner, "key")
			if cl != nil {
				cl.Close()
			}
			if e == nil {
				t.Fatal("証明書で認証成功")
			}
			if candidates.Load() != 1 || verified.Load() != 0 || dials.Load() != 0 {
				t.Fatalf("実wire観測が不正: candidate=%d verified=%d dial=%d", candidates.Load(), verified.Load(), dials.Load())
			}
			t.Log("有効に署名した証明書candidateを実SSHで1件観測し拒否、verified/dial=0。秘密鍵はメモリ内のみ")
		})
	}
}

func TestCloseoutN11CanonicalExtensionRequestsDenied(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			cfg, signer := config(t, mode)
			var dials atomic.Int32
			s, e := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
				dials.Add(1)
				c, raw, e := fixtureTarget(ctx, f.l.Addr().String())
				if e != nil {
					return nil, nil, e
				}
				return c, tcpCutoff(raw), nil
			})
			if e != nil {
				t.Fatal(e)
			}
			address := start(t, s, cfg)
			cl, e := client(address, cfg, signer, "key")
			if e != nil {
				t.Fatal(e)
			}
			defer cl.Close()
			path := ssh.Marshal(struct{ Path string }{"memory-only-fixture.sock"})
			for _, name := range []string{"streamlocal-forward@openssh.com", "cancel-streamlocal-forward@openssh.com"} {
				ok, _, e := cl.SendRequest(name, true, path)
				if e != nil || ok {
					t.Fatalf("global要求拒否不正: %s %v %v", name, ok, e)
				}
				if _, _, e = cl.SendRequest(name, false, path); e != nil {
					t.Fatal(e)
				}
			}
			// replyなしを成功応答と誤認しない。後続replyが返るまで同じglobal queueを消化する。
			if ok, _, e := cl.SendRequest("fixture-barrier", true, nil); e != nil || ok {
				t.Fatal("global barrier拒否なし")
			}
			if dials.Load() != 0 {
				t.Fatal("拒否global要求でtarget接続")
			}
			ch, e := open(cl, "127.0.0.1", 22)
			if e != nil {
				t.Fatal(e)
			}
			defer ch.Close()
			requests := []struct {
				name    string
				payload []byte
			}{
				{"window-change", ssh.Marshal(struct{ Columns, Rows, Width, Height uint32 }{80, 24, 0, 0})},
				{"signal", ssh.Marshal(struct{ Name string }{"TERM"})},
				{"break", ssh.Marshal(struct{ Milliseconds uint32 }{1})},
				{"xon-xoff", ssh.Marshal(struct{ Enabled bool }{true})},
				{"eow@openssh.com", nil},
			}
			for _, request := range requests {
				ok, e := ch.SendRequest(request.name, true, request.payload)
				if e != nil || ok {
					t.Fatalf("channel要求拒否不正: %s %v %v", request.name, ok, e)
				}
				if _, e = ch.SendRequest(request.name, false, request.payload); e != nil {
					t.Fatal(e)
				}
			}
			if ok, e := ch.SendRequest("fixture-barrier", true, nil); e != nil || ok {
				t.Fatal("channel barrier拒否なし")
			}
			echo(t, ch)
			if dials.Load() != 1 {
				t.Fatal("追加target副作用")
			}
			t.Log("正常payloadのglobal拡張2種/channel制御5種をreply有/無で送信。拒否replyのbarrier後に既存fixture転送だけ継続")
		})
	}
}
