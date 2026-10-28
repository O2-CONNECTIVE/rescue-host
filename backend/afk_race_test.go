package backend

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func awaitSnapshot(t *testing.T, s *Session, condition func(Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition(s.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("状態待ちが1秒超過: %+v", s.Snapshot())
}

func TestAFKUnit1HandshakeBudget(t *testing.T) {
	cfg, signer := config(t, "restricted")
	cfg.MaxHandshakes = 1
	var dials atomic.Int64
	s, err := newSession(cfg, func(context.Context) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("認証試験でdial禁止")
	})
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s, cfg)
	held, err := testDial(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Handshakes == 1 })
	next, err := client(address, cfg, signer, "key")
	if next != nil {
		next.Close()
	}
	if err == nil {
		t.Fatal("認証中の上限を回避")
	}
	held.Close()
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Handshakes == 0 && x.Owned == 0 })
	allowed, err := client(address, cfg, signer, "key")
	if err != nil {
		t.Fatal("正常解放後に認証枠を再利用できない")
	}
	defer allowed.Close()
	s.Revoke(cfg.Identity)
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Owned == 0 })
	if dials.Load() != 0 {
		t.Fatal("認証のみで転送先へ接続")
	}
}

func TestAFKUnit1SessionIsolation(t *testing.T) {
	fA, fB := fixture(t), fixture(t)
	cfgA, keyA := config(t, "restricted")
	cfgB, keyB := config(t, "full")
	cfgB.Identity.Epoch = "different-epoch"
	newFixtureSession := func(cfg Config, f *echoFixture) *Session {
		s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
			conn, raw, err := fixtureTarget(ctx, f.l.Addr().String())
			if err != nil {
				return nil, nil, err
			}
			return conn, tcpCutoff(raw), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	sA, sB := newFixtureSession(cfgA, fA), newFixtureSession(cfgB, fB)
	addressA, addressB := start(t, sA, cfgA), start(t, sB, cfgB)
	clA, err := client(addressA, cfgA, keyA, "key")
	if err != nil {
		t.Fatal(err)
	}
	defer clA.Close()
	clB, err := client(addressB, cfgB, keyB, "key")
	if err != nil {
		t.Fatal(err)
	}
	defer clB.Close()
	chA, err := open(clA, "127.0.0.1", 22)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, chA)
	chB, err := open(clB, "127.0.0.1", 22)
	if err != nil {
		t.Fatal(err)
	}
	defer chB.Close()
	echo(t, chB)
	badEpoch := cfgB.Identity
	badEpoch.Epoch = "stale-only-epoch"
	for _, bad := range []Identity{badEpoch, cfgA.Identity, {LaunchID: cfgB.Identity.LaunchID, Epoch: cfgB.Identity.Epoch, PolicyDigest: cfgA.Identity.PolicyDigest}} {
		if sB.Renew(bad, 1) == nil || sB.Revoke(bad) == nil {
			t.Fatal("別epoch/digestの制御が波及")
		}
	}
	wrong, err := client(addressB, cfgB, keyA, "key")
	if wrong != nil {
		wrong.Close()
	}
	if err == nil {
		t.Fatal("別起動の鍵を許可")
	}
	trigger := time.Now()
	sA.Revoke(cfgA.Identity)
	done := make(chan error, 1)
	go func() { var b [1]byte; _, e := chA.Read(b[:]); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("Aが切断されない")
		}
	case <-time.After(time.Second):
		t.Fatal("A失効が1秒超過")
	}
	select {
	case <-fA.events:
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("Aのpeer終端が1秒超過")
	}
	if sB.Snapshot().State != "active" || sB.Renew(cfgB.Identity, 1) != nil {
		t.Fatal("Bの認可へ波及")
	}
	echo(t, chB)
	if sA.Renew(cfgA.Identity, 2) == nil {
		t.Fatal("Aが再有効化")
	}
}

func TestAFKUnit1OpenRenewRevokeBarrier(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			cfg, signer := config(t, mode)
			cfg.MaxChannels = 1
			entered, release := make(chan struct{}), make(chan struct{})
			s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
				close(entered)
				<-release
				conn, raw, err := fixtureTarget(context.Background(), f.l.Addr().String())
				if err != nil {
					return nil, nil, err
				}
				return conn, tcpCutoff(raw), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			address := start(t, s, cfg)
			t.Cleanup(func() { onceRelease(release) })
			cl, err := client(address, cfg, signer, "key")
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			opened := make(chan error, 1)
			go func() {
				ch, e := open(cl, "127.0.0.1", 22)
				if ch != nil {
					ch.Close()
				}
				opened <- e
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("dial barrierに入らない")
			}
			barrier := make(chan struct{})
			var wg sync.WaitGroup
			for n := 0; n < 4; n++ {
				wg.Add(1)
				go func(seq uint64) { defer wg.Done(); <-barrier; s.Renew(cfg.Identity, seq) }(uint64(n))
			}
			wg.Add(1)
			go func() { defer wg.Done(); <-barrier; s.Revoke(cfg.Identity) }()
			close(barrier)
			wg.Wait()
			if s.Snapshot().State != "revoking" || s.Snapshot().Pending != 1 {
				t.Fatal("確定点で所有参照を失った")
			}
			for n := uint64(5); n < 9; n++ {
				if s.Renew(cfg.Identity, n) == nil {
					t.Fatal("失効確定後の更新")
				}
			}
			close(release)
			select {
			case e := <-opened:
				if e == nil {
					t.Fatal("失効確定後にchannelを公開")
				}
			case <-time.After(time.Second):
				t.Fatal("channel拒否が遅い")
			}
			select {
			case <-f.events:
			case <-time.After(time.Second):
				t.Fatal("遅延TCP終端なし")
			}
			closeSession(t, s)
			snap := s.Snapshot()
			if snap.Owned != 0 || snap.Pending != 0 || snap.Active != 0 {
				t.Fatal("終了後残留")
			}
		})
	}
}

// 既存ライブラリの署名だけをメモリ内で保持し、別transportへ再利用する負例。
// 署名/鍵のbytesはログ・ファイルへ出力しない。
type replaySignature struct {
	ssh.Signer
	mu          sync.Mutex
	signature   *ssh.Signature
	replayCount int
}

func (s *replaySignature) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signature != nil {
		s.replayCount++
		return &ssh.Signature{Format: s.signature.Format, Blob: append([]byte(nil), s.signature.Blob...)}, nil
	}
	sig, err := s.Signer.Sign(random, data)
	if err == nil {
		s.signature = &ssh.Signature{Format: sig.Format, Blob: append([]byte(nil), sig.Blob...)}
	}
	return sig, err
}
func TestAFKUnit1CrossTransportSignatureReplay(t *testing.T) {
	cfg, signer := config(t, "restricted")
	var dials atomic.Int64
	s, err := newSession(cfg, func(context.Context) (net.Conn, error) { dials.Add(1); return nil, errors.New("認証のみ") })
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s, cfg)
	replay := &replaySignature{Signer: signer}
	first, err := client(address, cfg, replay, "key")
	if err != nil {
		t.Fatal("最初の有効署名が失敗")
	}
	first.Close()
	second, err := client(address, cfg, replay, "key")
	if second != nil {
		second.Close()
	}
	if err == nil {
		t.Fatal("別transport署名の再利用を許可")
	}
	replay.mu.Lock()
	count := replay.replayCount
	replay.mu.Unlock()
	if count != 1 {
		t.Fatalf("署名再利用まで到達していない: %d", count)
	}
	if dials.Load() != 0 {
		t.Fatal("認証負例でdial")
	}
}
func TestAFKUnit1VerifiedReturnRevocation(t *testing.T) {
	cfg, signer := config(t, "full")
	var dials atomic.Int64
	s, err := newSession(cfg, func(context.Context) (net.Conn, error) { dials.Add(1); return nil, errors.New("認証のみ") })
	if err != nil {
		t.Fatal(err)
	}
	original := s.config.VerifiedPublicKeyCallback
	entered, release := make(chan struct{}), make(chan struct{})
	s.config.VerifiedPublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey, p *ssh.Permissions, algorithm string) (*ssh.Permissions, error) {
		result, err := original(meta, key, p, algorithm)
		if err != nil {
			return nil, err
		}
		close(entered)
		<-release
		return result, nil
	}
	address := start(t, s, cfg)
	t.Cleanup(func() { onceRelease(release) })
	done := make(chan error, 1)
	go func() {
		cl, e := client(address, cfg, signer, "key")
		if cl != nil {
			cl.Close()
		}
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("署名検証完了に到達しない")
	}
	s.Revoke(cfg.Identity)
	awaitSnapshot(t, s, func(x Snapshot) bool { return x.Owned == 0 })
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("失効後のtransportが利用可能")
		}
	case <-time.After(time.Second):
		t.Fatal("認証終了超過")
	}
	if dials.Load() != 0 {
		t.Fatal("失効認証からdial")
	}
}
