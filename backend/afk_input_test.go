package backend

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/forward"
	"golang.org/x/crypto/ssh"
)

func boundedBadPayloads() [][]byte {
	base := ssh.Marshal(wireTarget{"127.0.0.1", 22, "x", 1})
	out := [][]byte{{255, 255, 255, 255}, make([]byte, 1025)}
	for n := 0; n < len(base); n++ {
		out = append(out, append([]byte(nil), base[:n]...))
	}
	for n := 1; n <= 8; n++ {
		out = append(out, append(append([]byte(nil), base...), make([]byte, n)...))
	}
	for _, host := range []string{"127.0.\x000.1", "１２７.０.０.１", "127.0.0.1\n", "\t127.0.0.1", "127.0.0.1\r", "127.0.0.１", "127.0.0.1.", "[127.0.0.1]"} {
		out = append(out, ssh.Marshal(wireTarget{host, 22, "x", 1}))
	}
	return out
}

func TestAFKUnit2BoundedMutationCorpus(t *testing.T) {
	const seed int64 = 20261027
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			policy, err := forward.NewPolicy(mode)
			if err != nil {
				t.Fatal(err)
			}
			corpus := boundedBadPayloads()
			base := ssh.Marshal(wireTarget{"127.0.0.1", 22, "x", 1})
			rng := rand.New(rand.NewSource(seed))
			for n := 0; n < 256; n++ {
				value := append([]byte(nil), base...)
				// host本体かport本体だけを必ず変更し、originatorの許可とは混同しない。
				index := 4 + rng.Intn(len("127.0.0.1")+4)
				value[index] ^= byte(1 + rng.Intn(255))
				corpus = append(corpus, value)
			}
			for n, payload := range corpus {
				if policy.Permit("direct-tcpip", payload) {
					t.Fatalf("不正payloadを許可: index=%d seed=%d", n, seed)
				}
			}
			emptyOrigin := ssh.Marshal(wireTarget{"127.0.0.1", 22, "", 0})
			exact := ssh.Marshal(wireTarget{"127.0.0.1", 22, strings.Repeat("x", 1024-len(emptyOrigin)), 0xffffffff})
			if len(exact) != 1024 || !policy.Permit("direct-tcpip", exact) || policy.Permit("direct-tcpip", append(exact, 0)) {
				t.Fatal("1KiB境界が不正")
			}
			t.Logf("seed=%d bounded_rejected_payloads=%d maximum_input_bytes=1025 canonical_exact_1024_allowed=true", seed, len(corpus))
		})
	}
}

func TestAFKUnit2WireRejectionAndDrain(t *testing.T) {
	for _, mode := range []string{"restricted", "full"} {
		t.Run(mode, func(t *testing.T) {
			cfg, signer := config(t, mode)
			f := fixture(t)
			var dials atomic.Int64
			s, err := newSessionWithCutoff(cfg, func(ctx context.Context) (net.Conn, func() error, error) {
				dials.Add(1)
				conn, raw, err := fixtureTarget(ctx, f.l.Addr().String())
				if err != nil {
					return nil, nil, err
				}
				return conn, tcpCutoff(raw), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			address := start(t, s, cfg)
			cl, err := client(address, cfg, signer, "key")
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			for _, payload := range boundedBadPayloads() {
				ch, _, err := cl.OpenChannel("direct-tcpip", payload)
				if ch != nil {
					ch.Close()
				}
				if err == nil {
					t.Fatal("wire不正payloadを許可")
				}
			}
			if dials.Load() != 0 {
				t.Fatal("拒否時にdial")
			}
			for _, kind := range []string{"streamlocal-forward@openssh.com", "cancel-streamlocal-forward@openssh.com", "keepalive@openssh.com", "afk-unknown"} {
				for _, reply := range []bool{false, true} {
					ok, _, err := cl.SendRequest(kind, reply, []byte{0, 255, 0})
					if err != nil || ok {
						t.Fatal("global要求拒否/drain失敗")
					}
				}
			}
			channel, err := open(cl, "127.0.0.1", 22)
			if err != nil {
				t.Fatal("不正要求後に有効な要求が停止")
			}
			defer channel.Close()
			echo(t, channel)
			for _, kind := range []string{"shell", "exec", "subsystem", "env", "window-change", "afk-unknown"} {
				for _, reply := range []bool{false, true} {
					ok, err := channel.SendRequest(kind, reply, []byte{255, 0})
					if err != nil || ok {
						t.Fatal("channel要求拒否/drain失敗")
					}
				}
			}
			echo(t, channel)
			if dials.Load() != 1 {
				t.Fatal("要求から追加接続が生じた")
			}
			// 少数の不明要求と失効を並行。大量要求の負荷試験ではない。
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				for i := 0; i < 16; i++ {
					if _, _, err := cl.SendRequest("afk-bounded", true, nil); err != nil {
						return
					}
				}
			}()
			trigger := time.Now()
			s.Revoke(cfg.Identity)
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("要求処理で失効が飢餓")
			}
			select {
			case event := <-f.events:
				if e, ok := event.err.(net.Error); ok && e.Timeout() {
					t.Fatal("timeoutを切断と誤認")
				}
			case <-time.After(time.Until(trigger.Add(time.Second))):
				t.Fatal("peer終了1秒超過")
			}
			t.Logf("wire_rejected=%d global_checks=8 channel_checks=12 bounded_drain_requests<=16", len(boundedBadPayloads()))
		})
	}
}

func TestAFKUnit2ConfigurationBoundaries(t *testing.T) {
	for _, which := range []string{"handshake-high", "handshake-negative", "channels-high", "channels-negative", "max-zero", "max-negative", "lease-zero", "lease-negative", "lease-long", "launch-empty", "launch-long", "epoch-empty", "epoch-tab", "digest-upper", "digest-short", "wrong-mode-digest", "unknown-mode", "nine-keys", "certificate-key"} {
		t.Run(which, func(t *testing.T) {
			c, a := config(t, "restricted")
			switch which {
			case "handshake-high":
				c.MaxHandshakes = 65
			case "handshake-negative":
				c.MaxHandshakes = -1
			case "channels-high":
				c.MaxChannels = 33
			case "channels-negative":
				c.MaxChannels = -1
			case "max-zero":
				c.MaximumDuration = 0
			case "max-negative":
				c.MaximumDuration = -1
			case "lease-zero":
				c.LeaseDuration = 0
			case "lease-negative":
				c.LeaseDuration = -1
			case "lease-long":
				c.LeaseDuration = c.MaximumDuration + time.Nanosecond
			case "launch-empty":
				c.Identity.LaunchID = ""
			case "launch-long":
				c.Identity.LaunchID = strings.Repeat("x", 129)
			case "epoch-empty":
				c.Identity.Epoch = ""
			case "epoch-tab":
				c.Identity.Epoch = "bad\tepoch"
			case "digest-upper":
				c.Identity.PolicyDigest = strings.ToUpper(c.Identity.PolicyDigest)
			case "digest-short":
				c.Identity.PolicyDigest = c.Identity.PolicyDigest[:62]
			case "wrong-mode-digest":
				c.OperationMode = "full"
			case "unknown-mode":
				c.OperationMode = "admin"
			case "nine-keys":
				c.AllowedKeys = make([]ssh.PublicKey, 9)
				for n := range c.AllowedKeys {
					c.AllowedKeys[n] = a.PublicKey()
				}
			case "certificate-key":
				c.AllowedKeys = []ssh.PublicKey{&ssh.Certificate{Key: a.PublicKey(), CertType: ssh.UserCert}}
			}
			s, err := New(c)
			if err == nil {
				closeSession(t, s)
				t.Fatal("境界外configを許可")
			}
		})
	}
	// nilのtest adapterが設定入口になることも拒否する。
	if s, err := newSession(Config{}, nil); s != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("nil adapterを許可")
	}
}
