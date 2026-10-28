package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"golang.org/x/crypto/ssh"
)

// FakeSigner は公開 RFC8032 vector のみ。秘密鍵や実 Sign/Load を使いません。
type receiverFakeSigner struct{ public ssh.PublicKey }

func (s receiverFakeSigner) PublicKey() ssh.PublicKey { return s.public }
func (receiverFakeSigner) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	return nil, errors.New("fake signer has no secret")
}

type receiverPendingReader struct {
	started, release chan struct{}
	ready            atomic.Bool
	closes           atomic.Int32
}

func (p *receiverPendingReader) ReadOnce(ctx context.Context) ([]byte, error) {
	close(p.started)
	<-ctx.Done()
	<-p.release
	p.ready.Store(true)
	return nil, ctx.Err()
}
func (p *receiverPendingReader) Close(context.Context) error {
	p.closes.Add(1)
	if !p.ready.Load() {
		return bootstrapio.ErrUnconfirmed
	}
	return nil
}

func TestNativeReadApprovedConcurrentCloseCancelsAndRetainsPendingReader(t *testing.T) {
	f := nativeReceiverFixture(t)
	p := &receiverPendingReader{started: make(chan struct{}), release: make(chan struct{})}
	f.r.ops.adopt = func(context.Context, uintptr, time.Duration) (receiverReader, error) { return p, nil }
	ctx := receiverContext(t)
	type result struct {
		config runbackend.ApprovedConfig
		err    error
	}
	done := make(chan result, 1)
	go func() { c, e := f.r.ReadApproved(ctx, f.args, f.stdio); done <- result{c, e} }()
	select {
	case <-p.started:
	case <-ctx.Done():
		t.Fatal("fake read did not start")
	}
	if err := f.r.Close(ctx); !errors.Is(err, ErrNativeUnconfirmed) || !errors.Is(err, bootstrapio.ErrUnconfirmed) {
		t.Fatalf("pending close lost ownership: %v", err)
	}
	select {
	case <-done:
		t.Fatal("returned before pending fake completion")
	default:
	}
	close(p.release)
	select {
	case got := <-done:
		if got.err == nil || !reflect.DeepEqual(got.config, runbackend.ApprovedConfig{}) {
			t.Fatal("canceled read approved")
		}
	case <-ctx.Done():
		t.Fatal("fake read did not finish")
	}
	if err := f.r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if p.closes.Load() != 2 {
		t.Fatalf("pending reader was lost or closed again: %d", p.closes.Load())
	}
	for _, event := range f.events {
		if event == "load" || event == "key-scope" {
			t.Fatal("concurrent close reached key scope")
		}
	}
}

type receiverFakePeer struct {
	events               *[]string
	recheckErr, closeErr error
	observe              func(context.Context)
}

func (p *receiverFakePeer) Recheck(ctx context.Context) error {
	if p.observe != nil {
		p.observe(ctx)
	}
	*p.events = append(*p.events, "recheck")
	return p.recheckErr
}
func (p *receiverFakePeer) Close(context.Context) error {
	*p.events = append(*p.events, "peer-close")
	return p.closeErr
}

type receiverFakeReader struct {
	events            *[]string
	record            []byte
	readErr, closeErr error
	onRead            func()
}

func (p *receiverFakeReader) ReadOnce(context.Context) ([]byte, error) {
	*p.events = append(*p.events, "read", "eof")
	if p.onRead != nil {
		p.onRead()
	}
	return append([]byte(nil), p.record...), p.readErr
}
func (p *receiverFakeReader) Close(context.Context) error {
	*p.events = append(*p.events, "reader-close")
	return p.closeErr
}

type receiverFakeScope struct {
	events   *[]string
	ctx      context.Context
	closeErr error
}

func (s *receiverFakeScope) Context() context.Context { return s.ctx }
func (s *receiverFakeScope) Close() error {
	*s.events = append(*s.events, "scope-close")
	return s.closeErr
}

type receiverFixture struct {
	r      *NativeReceiver
	config NativeReceiverConfig
	args   []string
	stdio  [3]uintptr
	events []string
	peer   *receiverFakePeer
	reader *receiverFakeReader
	scope  *receiverFakeScope
	signer ssh.Signer
}

func nativeReceiverFixture(t *testing.T) *receiverFixture {
	t.Helper()
	public, _ := hex.DecodeString("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	wireKey := append([]byte{0, 0, 0, 11}, []byte("ssh-ed25519")...)
	wireKey = append(wireKey, 0, 0, 0, 32)
	wireKey = append(wireKey, public...)
	key, err := ssh.ParsePublicKey(wireKey)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{key})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := backend.PolicyDigest("restricted")
	service := "S-1-5-80-1-2-3-4-5"
	operator := "S-1-5-21-1-2-3-1001"
	volume := protectedkey.VolumeProfile{Root: `C:\`, Serial: 7, Filesystem: "NTFS"}
	nodes := func(path string) []protectedkey.NodePolicy {
		return []protectedkey.NodePolicy{{Path: `C:\`, OwnerSID: operator, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}}, {Path: path, OwnerSID: operator, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}}}
	}
	keys := protectedkey.Profile{Version: 1, ProcessUserSID: "S-1-5-18", ServiceSID: service, OperatorSID: operator, GuestSID: "S-1-5-21-1-2-3-1002", Volume: volume, Provisioning: protectedkey.ProvisioningRecord{Reference: "synthetic", ProtectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Catalog: map[string]protectedkey.KeyProfile{"host_key_1": {Path: `C:\key`, Fingerprint: ssh.FingerprintSHA256(key), Nodes: nodes(`C:\key`)}}}
	image := func(path string) bootstrapauth.Image {
		return bootstrapauth.Image{Path: path, SHA256: [32]byte{1}, MaxBytes: 1024, Volume: volume, Nodes: nodes(path)}
	}
	c := NativeReceiverConfig{Profile: bootstrapauth.Profile{Keys: keys, ServiceName: "SyntheticService", SupervisorImage: image(`C:\service.exe`), ChildImage: image(`C:\child.exe`)}, Limits: ReceiverLimits{MaxFrames: 100, MaxCleanupTimeout: time.Second}, MaximumDuration: time.Minute, LeaseCeiling: 5 * time.Second, MaxChannels: 8, MaxHandshakes: 8, MaxTransports: 8, StartupTimeout: 2 * time.Second}
	r, err := NewNativeReceiver(c)
	if err != nil {
		t.Fatal(err)
	}
	f := &receiverFixture{r: r, config: c, stdio: [3]uintptr{11, 12, 13}, signer: receiverFakeSigner{key}}
	f.events = []string{}
	f.peer = &receiverFakePeer{events: &f.events}
	f.reader = &receiverFakeReader{events: &f.events}
	f.scope = &receiverFakeScope{events: &f.events}
	// 独立 literal。期限を再基準化せず、epoch=0 と元の絶対期限を保持します。
	f.reader.record = []byte(fmt.Sprintf(`{"protocol":"rescue-host-bootstrap/1","issuerInstanceEpoch":"%s","grant":{"generation":"1","identity":{"launchId":"launch_1","epoch":"0","policyDigest":"%s"},"keySetDigest":"%x","guestBoundary":"separated_non_admin_guest","hardDeadline":"2100-01-01T12:00:20.000000123Z","backendLeaseCeilingNs":"3000000000"},"binding":{"artifactSha256":"%s","channelNonce":"%s"},"policyRef":"policy_1","backend":{"operationMode":"restricted","allowedKeys":["%s"],"maximumDurationNs":"20000000000","leaseDurationNs":"1000000000","maxChannels":"2","maxHandshakes":"3","maxTransports":"4"},"hostKeyRef":"host_key_1","stdio":{"frames":"10","cleanupTimeoutNs":"1000000000"}}`+"\n", strings.Repeat("c", 64), policy, digest, strings.Repeat("a", 64), strings.Repeat("b", 64), base64.StdEncoding.EncodeToString(wireKey)))
	f.args = []string{"--supervised-internal", "--bootstrap-handle=99", "--bootstrap-issuer=" + strings.Repeat("c", 64), "--bootstrap-generation=1", "--bootstrap-launch-id=launch_1", "--bootstrap-session-epoch=0", "--bootstrap-policy-digest=" + policy, "--bootstrap-artifact-sha256=" + strings.Repeat("a", 64), "--bootstrap-channel-nonce=" + strings.Repeat("b", 64), "--bootstrap-policy-ref=policy_1"}
	r.ops = nativeReceiverOps{
		authenticate: func(ctx context.Context, h uintptr) (receiverPeer, error) {
			f.events = append(f.events, "auth")
			if h != 99 {
				t.Fatal("wrong locator")
			}
			return f.peer, nil
		},
		adopt: func(ctx context.Context, h uintptr, d time.Duration) (receiverReader, error) {
			f.events = append(f.events, "adopt")
			return f.reader, nil
		},
		begin: func(ctx context.Context, p protectedkey.Profile) (receiverScope, error) {
			f.events = append(f.events, "key-scope")
			f.scope.ctx = ctx
			return f.scope, nil
		},
		load: func(ctx context.Context, ref string) (ssh.Signer, error) {
			f.events = append(f.events, "load")
			if ref != "host_key_1" {
				t.Fatal("unregistered key")
			}
			return f.signer, nil
		},
		closeHandle: func(context.Context, uintptr) error { f.events = append(f.events, "raw-close"); return nil },
	}
	return f
}
func receiverContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestNativeReadApprovedAuthenticatesReadsAndCleansBeforeReturning(t *testing.T) {
	f := nativeReceiverFixture(t)
	got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
	if err != nil {
		t.Fatalf("configured authenticated input refused: %v; events=%v", err, f.events)
	}
	if got.Backend.HostKey == nil || got.Grant.Binding.Identity.Epoch != "0" || got.Backend.MaxTransports != 4 || got.Limits.Frames != 10 || got.Grant.HardDeadline.Format(time.RFC3339Nano) != "2100-01-01T12:00:20.000000123Z" {
		t.Fatal("approved config lost authenticated values")
	}
	want := []string{"auth", "adopt", "read", "eof", "recheck", "key-scope", "load", "recheck", "scope-close", "peer-close", "reader-close"}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("phase order=%v, want=%v", f.events, want)
	}
	if err := f.r.Close(receiverContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio); !errors.Is(err, ErrNativeConsumed) {
		t.Fatal("receiver retried")
	}
}

func TestNativeReadApprovedRechecksPeerAfterFailedKeyLoad(t *testing.T) {
	f := nativeReceiverFixture(t)
	f.r.ops.load = func(context.Context, string) (ssh.Signer, error) {
		f.events = append(f.events, "load")
		return nil, protectedkey.ErrDenied
	}
	got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
	if !errors.Is(err, protectedkey.ErrDenied) || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) {
		t.Fatal("failed key load returned approval")
	}
	want := []string{"auth", "adopt", "read", "eof", "recheck", "key-scope", "load", "recheck", "scope-close", "peer-close", "reader-close"}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("failed load skipped held-peer recheck: %v", f.events)
	}
}

func TestNativeReadApprovedCancellationBeforeKeysNeverLoads(t *testing.T) {
	f := nativeReceiverFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f.reader.onRead = cancel
	got, err := f.r.ReadApproved(ctx, f.args, f.stdio)
	if err == nil || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) {
		t.Fatal("canceled input approved")
	}
	for _, event := range f.events {
		if event == "load" || event == "key-scope" {
			t.Fatalf("canceled read reached key phase: %v", f.events)
		}
	}
	if _, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio); !errors.Is(err, ErrNativeConsumed) {
		t.Fatal("cancellation allowed retry")
	}
}

// 最初の成功 slice と既存 strict codec に対する境界回帰です。
func TestNativeReadApprovedRejectsBeforeKeys(t *testing.T) {
	for name, mutate := range map[string]func(*receiverFixture){
		"issuer":        func(f *receiverFixture) { f.args[2] = "--bootstrap-issuer=" + strings.Repeat("d", 64) },
		"generation":    func(f *receiverFixture) { f.args[3] = "--bootstrap-generation=2" },
		"launch":        func(f *receiverFixture) { f.args[4] = "--bootstrap-launch-id=other" },
		"epoch":         func(f *receiverFixture) { f.args[5] = "--bootstrap-session-epoch=1" },
		"policy digest": func(f *receiverFixture) { f.args[6] = "--bootstrap-policy-digest=" + strings.Repeat("e", 64) },
		"artifact":      func(f *receiverFixture) { f.args[7] = "--bootstrap-artifact-sha256=" + strings.Repeat("d", 64) },
		"nonce":         func(f *receiverFixture) { f.args[8] = "--bootstrap-channel-nonce=" + strings.Repeat("d", 64) },
		"policy ref":    func(f *receiverFixture) { f.args[9] = "--bootstrap-policy-ref=other" },
		"mode": func(f *receiverFixture) {
			f.reader.record = []byte(strings.ReplaceAll(string(f.reader.record), `"operationMode":"restricted"`, `"operationMode":"full"`))
		},
		"digest": func(f *receiverFixture) {
			f.reader.record = []byte(strings.ReplaceAll(string(f.reader.record), `"allowedKeys":["`, `"allowedKeys":["bad`))
		},
		"unknown field": func(f *receiverFixture) {
			f.reader.record = []byte(strings.Replace(string(f.reader.record), `{"protocol"`, `{"secret":"x","protocol"`, 1))
		},
		"duplicate field": func(f *receiverFixture) {
			f.reader.record = []byte(strings.Replace(string(f.reader.record), `{"protocol"`, `{"policyRef":"policy_1","protocol"`, 1))
		},
		"extra record":       func(f *receiverFixture) { f.reader.record = append(f.reader.record, []byte("{}\n")...) },
		"missing EOF":        func(f *receiverFixture) { f.reader.readErr = bootstrapio.ErrFraming },
		"frames":             func(f *receiverFixture) { f.r.c.Limits.MaxFrames = 9 },
		"cleanup ceiling":    func(f *receiverFixture) { f.r.c.Limits.MaxCleanupTimeout = time.Millisecond },
		"maximum ceiling":    func(f *receiverFixture) { f.r.c.MaximumDuration = 19 * time.Second },
		"lease ceiling":      func(f *receiverFixture) { f.r.c.LeaseCeiling = 2 * time.Second },
		"channels ceiling":   func(f *receiverFixture) { f.r.c.MaxChannels = 1 },
		"handshakes ceiling": func(f *receiverFixture) { f.r.c.MaxHandshakes = 2 },
		"transports ceiling": func(f *receiverFixture) { f.r.c.MaxTransports = 3 },
		"catalog": func(f *receiverFixture) {
			f.reader.record = []byte(strings.ReplaceAll(string(f.reader.record), `"hostKeyRef":"host_key_1"`, `"hostKeyRef":"unregistered"`))
		},
		"peer before key": func(f *receiverFixture) { f.peer.recheckErr = bootstrapauth.ErrDenied },
	} {
		t.Run(name, func(t *testing.T) {
			f := nativeReceiverFixture(t)
			mutate(f)
			got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
			if err == nil || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) {
				t.Fatal("invalid bootstrap approved")
			}
			for _, event := range f.events {
				if event == "load" || event == "key-scope" {
					t.Fatalf("invalid bootstrap reached keys: %v", f.events)
				}
			}
			before := append([]string(nil), f.events...)
			if _, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio); !errors.Is(err, ErrNativeConsumed) || !reflect.DeepEqual(before, f.events) {
				t.Fatal("rejected receive retried")
			}
		})
	}
}

func TestNativeReadApprovedAuthenticationFailureNeverAdoptsOrClosesLocator(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		f := nativeReceiverFixture(t)
		denied := bootstrapauth.ErrDenied
		if fatal {
			denied = bootstrapauth.ErrFatal
		}
		f.r.ops.authenticate = func(context.Context, uintptr) (receiverPeer, error) {
			f.events = append(f.events, "auth")
			return f.peer, denied
		}
		got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
		if !errors.Is(err, denied) || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) || !reflect.DeepEqual(f.events, []string{"auth", "peer-close"}) {
			t.Fatalf("auth denial escaped boundary: %v %v", err, f.events)
		}
		closeErr := f.r.Close(receiverContext(t))
		if fatal && !errors.Is(closeErr, bootstrapauth.ErrFatal) {
			t.Fatal("fatal became nonfatal")
		}
	}
}

func TestNativeReadApprovedAdoptionFailureRetainsAuthenticatedHandle(t *testing.T) {
	f := nativeReceiverFixture(t)
	f.r.ops.adopt = func(context.Context, uintptr, time.Duration) (receiverReader, error) {
		f.events = append(f.events, "adopt")
		return nil, bootstrapio.ErrInvalid
	}
	attempts := 0
	f.r.ops.closeHandle = func(context.Context, uintptr) error {
		attempts++
		f.events = append(f.events, "raw-close")
		if attempts == 1 {
			return bootstrapio.ErrUnconfirmed
		}
		return nil
	}
	got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
	if !errors.Is(err, bootstrapio.ErrUnconfirmed) || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) {
		t.Fatal("failed adoption approved or lost cleanup error")
	}
	if err := f.r.Close(receiverContext(t)); err != nil || attempts != 2 {
		t.Fatal("authenticated raw handle lost before retry Close")
	}
	if err := f.r.Close(receiverContext(t)); err != nil || attempts != 2 {
		t.Fatal("raw handle closed twice")
	}
}

func TestNativeReadApprovedCleanupAndFinalPeerFailureNeverReturnConfig(t *testing.T) {
	for name, mutate := range map[string]func(*receiverFixture){
		"scope close":  func(f *receiverFixture) { f.scope.closeErr = protectedkey.ErrDenied },
		"scope fatal":  func(f *receiverFixture) { f.scope.closeErr = protectedkey.ErrFatal },
		"reader close": func(f *receiverFixture) { f.reader.closeErr = bootstrapio.ErrUnconfirmed },
		"peer close":   func(f *receiverFixture) { f.peer.closeErr = bootstrapauth.ErrUnconfirmed },
		"peer after load": func(f *receiverFixture) {
			old := f.r.ops.load
			f.r.ops.load = func(c context.Context, r string) (ssh.Signer, error) {
				signer, err := old(c, r)
				f.peer.recheckErr = bootstrapauth.ErrDenied
				return signer, err
			}
		},
		"nil signer": func(f *receiverFixture) { f.signer = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := nativeReceiverFixture(t)
			mutate(f)
			got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
			if err == nil || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) {
				t.Fatal("unclean/changed peer approved")
			}
			f.scope.closeErr = nil
			f.peer.closeErr = nil
			f.reader.closeErr = nil
			closeErr := f.r.Close(receiverContext(t))
			if name == "scope fatal" {
				if !errors.Is(closeErr, protectedkey.ErrFatal) {
					t.Fatal("fatal scope recovered")
				}
			} else if closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

func TestNativeReceiverConstructorRequiresProtectedProfileAndEveryCeiling(t *testing.T) {
	f := nativeReceiverFixture(t)
	for name, mutate := range map[string]func(*NativeReceiverConfig){
		"profile":           func(c *NativeReceiverConfig) { c.Profile = bootstrapauth.Profile{} },
		"frames":            func(c *NativeReceiverConfig) { c.Limits.MaxFrames = 0 },
		"cleanup":           func(c *NativeReceiverConfig) { c.Limits.MaxCleanupTimeout = 0 },
		"startup":           func(c *NativeReceiverConfig) { c.StartupTimeout = 0 },
		"maximum":           func(c *NativeReceiverConfig) { c.MaximumDuration = 0 },
		"lease":             func(c *NativeReceiverConfig) { c.LeaseCeiling = 0 },
		"channels":          func(c *NativeReceiverConfig) { c.MaxChannels = 0 },
		"handshakes":        func(c *NativeReceiverConfig) { c.MaxHandshakes = 0 },
		"transports":        func(c *NativeReceiverConfig) { c.MaxTransports = 0 },
		"global maximum":    func(c *NativeReceiverConfig) { c.MaximumDuration = 2*time.Hour + 1 },
		"global lease":      func(c *NativeReceiverConfig) { c.LeaseCeiling = time.Minute + 1 },
		"global channels":   func(c *NativeReceiverConfig) { c.MaxChannels = 33 },
		"global handshakes": func(c *NativeReceiverConfig) { c.MaxHandshakes = 65 },
		"global transports": func(c *NativeReceiverConfig) { c.MaxTransports = 65 },
	} {
		t.Run(name, func(t *testing.T) {
			c := f.config
			mutate(&c)
			if r, err := NewNativeReceiver(c); r != nil || err == nil {
				t.Fatal("unknown/missing protected input accepted")
			}
		})
	}
	if f.r.StartupTimeout() != 2*time.Second || f.r.CleanupTimeout() != time.Second {
		t.Fatal("configured budgets lost")
	}
	f.config.Profile.ChildImage.Nodes[0].ACL[0].SID = "mutated"
	k := f.config.Profile.Keys.Catalog["host_key_1"]
	k.Nodes[0].ACL[0].SID = "mutated"
	delete(f.config.Profile.Keys.Catalog, "host_key_1")
	got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
	if err != nil || got.Backend.HostKey == nil {
		t.Fatalf("caller mutation changed receiver: %v", err)
	}
}

func TestNativeReceiverZeroAndInvalidArgumentsCannotReachNative(t *testing.T) {
	var zero NativeReceiver
	if got, err := zero.ReadApproved(receiverContext(t), nil, [3]uintptr{}); !errors.Is(err, bootstrapauth.ErrConfig) || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) {
		t.Fatal("zero receiver crossed gate")
	}
	for _, ctx := range []context.Context{nil, context.Background()} {
		f := nativeReceiverFixture(t)
		if _, err := f.r.ReadApproved(ctx, f.args, f.stdio); err == nil || len(f.events) != 0 {
			t.Fatal("unbounded context crossed gate")
		}
	}
	f := nativeReceiverFixture(t)
	f.args[1] = "--bootstrap-handle=11"
	if _, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio); err == nil || len(f.events) != 0 {
		t.Fatal("stdio locator reached auth")
	}
}

func TestNativeReadApprovedExpiredGrantNeverStartsKeyRead(t *testing.T) {
	f := nativeReceiverFixture(t)
	f.reader.record = []byte(strings.ReplaceAll(string(f.reader.record), "2100-01-01T12:00:20.000000123Z", "2000-01-01T00:00:00Z"))
	got, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio)
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, runbackend.ApprovedConfig{}) {
		t.Fatalf("expired grant approved or lost expiry: %v", err)
	}
	for _, event := range f.events {
		if event == "recheck" || event == "key-scope" || event == "load" {
			t.Fatalf("expired grant reached privileged phase: %v", f.events)
		}
	}
	if _, err := f.r.ReadApproved(receiverContext(t), f.args, f.stdio); !errors.Is(err, ErrNativeConsumed) {
		t.Fatal("expired grant retried")
	}
}

func TestNativeReadApprovedUsesOriginalAbsoluteCapForPeerAndKeyScope(t *testing.T) {
	f := nativeReceiverFixture(t)
	c := f.config
	c.StartupTimeout = 100 * 365 * 24 * time.Hour
	r, err := NewNativeReceiver(c)
	if err != nil {
		t.Fatal(err)
	}
	r.ops = f.r.ops
	f.r = r
	want := time.Date(2100, 1, 1, 12, 0, 20, 123, time.UTC)
	ctx, cancel := context.WithDeadline(context.Background(), time.Date(2101, 1, 1, 0, 0, 0, 0, time.UTC))
	defer cancel()
	checks := 0
	check := func(ctx context.Context) {
		t.Helper()
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(want) {
			t.Fatalf("original hard cap changed: %v", deadline)
		}
		checks++
	}
	f.peer.observe = check
	oldBegin := f.r.ops.begin
	f.r.ops.begin = func(ctx context.Context, p protectedkey.Profile) (receiverScope, error) {
		check(ctx)
		return oldBegin(ctx, p)
	}
	oldLoad := f.r.ops.load
	f.r.ops.load = func(ctx context.Context, ref string) (ssh.Signer, error) { check(ctx); return oldLoad(ctx, ref) }
	got, err := f.r.ReadApproved(ctx, f.args, f.stdio)
	if err != nil || !got.Grant.HardDeadline.Equal(want) || checks != 4 {
		t.Fatalf("absolute cap not shared across both peer checks and key scope/load: checks=%d err=%v", checks, err)
	}
}
