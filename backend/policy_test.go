package backend

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPythonPolicyDigests(t *testing.T) {
	want := map[string]string{"restricted": "218fa8d3d295dccf58f443a934f4d3178f39d5c99c303829f5d6d090ec7a40a1", "full": "4052790d975bbf627a483f84fac3656a420e828dc671ed3fdcc9800deb1b54c0"}
	for mode, digest := range want {
		got, e := PolicyDigest(mode)
		if e != nil || got != digest {
			t.Fatalf("Python 方針との不一致: %s", mode)
		}
	}
	if _, e := PolicyDigest("admin"); e == nil {
		t.Fatal("未知のモードを許可")
	}
}

func TestIdentityAndRenewal(t *testing.T) {
	c, _ := config(t, "restricted")
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	defer closeSession(t, s)
	if e = s.Ready(c.Identity); e != nil {
		t.Fatal(e)
	}
	if e = s.Renew(c.Identity, 0); e != nil {
		t.Fatal("元モデルと同じ初回0が必要")
	}
	if s.Renew(c.Identity, 0) == nil {
		t.Fatal("重複連番を許可")
	}
	bad := c.Identity
	bad.Epoch = "stale"
	if s.Renew(bad, 1) == nil || s.Revoke(bad) == nil || s.Activate(bad) == nil {
		t.Fatal("別世代を許可")
	}
	if e = s.Activate(c.Identity); e != nil {
		t.Fatal(e)
	}
	if e = s.Renew(c.Identity, ^uint64(0)); e != nil {
		t.Fatal(e)
	}
	if s.Renew(c.Identity, 1) == nil {
		t.Fatal("逆順を許可")
	}
	s.Revoke(c.Identity)
	if s.Ready(c.Identity) == nil || s.Activate(c.Identity) == nil || s.Renew(c.Identity, ^uint64(0)) == nil {
		t.Fatal("失効後に復活")
	}
}

func TestKeySetSnapshot(t *testing.T) {
	c, a := config(t, "restricted")
	b := key(t)
	s, e := newSession(c, func(context.Context) (net.Conn, error) { return nil, errors.New("本番 dial を禁止") })
	if e != nil {
		t.Fatal(e)
	}
	c.AllowedKeys[0] = b.PublicKey()
	address := start(t, s, c)
	cl, e := client(address, c, a, "key")
	if e != nil {
		t.Fatal("元の鍵を失った")
	}
	cl.Close()
	cl, e = client(address, c, b, "key")
	if cl != nil {
		cl.Close()
	}
	if e == nil {
		t.Fatal("呼び出し元の書換で鍵が追加された")
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, which := range []string{"nil-host", "empty-keys", "bad-key", "zero-handshakes", "zero-channels", "long-duration", "invalid-id"} {
		t.Run(which, func(t *testing.T) {
			c, _ := config(t, "restricted")
			switch which {
			case "nil-host":
				c.HostKey = nil
			case "empty-keys":
				c.AllowedKeys = nil
			case "bad-key":
				c.AllowedKeys = []ssh.PublicKey{nil}
			case "zero-handshakes":
				c.MaxHandshakes = 0
			case "zero-channels":
				c.MaxChannels = 0
			case "long-duration":
				c.MaximumDuration = 3 * 60 * 60 * 1000000000
			case "invalid-id":
				c.Identity.LaunchID = "with space"
			}
			s, e := New(c)
			if e == nil {
				closeSession(t, s)
				t.Fatal("不正設定を許可")
			}
		})
	}
}
