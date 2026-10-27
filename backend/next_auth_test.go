package backend

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// 公開鍵照会には答えるが署名を返さない。鍵/署名bytesを保存・出力しない。
type queryOnlySigner struct {
	ssh.Signer
	attempts atomic.Int64
}

func (s *queryOnlySigner) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	s.attempts.Add(1)
	return nil, errors.New("試験clientは署名を送らず中断")
}

func TestNextN1UnsignedQueryDoesNotAuthenticate(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			cfg, signer := config(t, mode)
			var candidate, verified, dials atomic.Int64
			s, err := newSession(cfg, func(context.Context) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("認証のみ。target禁止")
			})
			if err != nil {
				t.Fatal(err)
			}
			originalCandidate, originalVerified := s.config.PublicKeyCallback, s.config.VerifiedPublicKeyCallback
			s.config.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				candidate.Add(1)
				return originalCandidate(meta, key)
			}
			s.config.VerifiedPublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey, p *ssh.Permissions, algorithm string) (*ssh.Permissions, error) {
				verified.Add(1)
				return originalVerified(meta, key, p, algorithm)
			}
			address := start(t, s, cfg)
			query := &queryOnlySigner{Signer: signer}
			cl, err := client(address, cfg, query, "key")
			if cl != nil {
				cl.Close()
			}
			if err == nil || cl != nil {
				t.Fatal("署名なし照会を認証成功にした")
			}
			awaitSnapshot(t, s, func(x Snapshot) bool { return x.Handshakes == 0 && x.Owned == 0 })
			if candidate.Load() < 1 || query.attempts.Load() != 1 || verified.Load() != 0 || dials.Load() != 0 {
				t.Fatalf("unsigned経路観測不一致: candidate=%d signing_attempts=%d verified=%d dials=%d", candidate.Load(), query.attempts.Load(), verified.Load(), dials.Load())
			}
			if snap := s.Snapshot(); snap.Pending != 0 || snap.Active != 0 {
				t.Fatal("署名なし認証からchannel枠が作られた")
			}
			t.Logf("unsigned_candidate=%d signing_attempts=1 signature_sent=0 verified=0 usable_transport=0 channel_reservations=0 target_dials=0 owned_after_close=0", candidate.Load())
			// 照会失敗で別の正規transportまで失効しない。
			valid, err := client(address, cfg, signer, "key")
			if err != nil {
				t.Fatal("正規署名の後続transportを拒否")
			}
			valid.Close()
		})
	}
}
