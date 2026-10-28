package servicehost

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

type nativeBaseFunc func(context.Context, supervisor.ProvisionedGrant) (ChildPlan, error)

func (f nativeBaseFunc) PrepareChild(c context.Context, g supervisor.ProvisionedGrant) (ChildPlan, error) {
	return f(c, g)
}

type nativeInput struct{ bytes.Buffer }

func (*nativeInput) Close() error { return nil }

type fakeServiceProbe struct{ events *[]string }

func (f *fakeServiceProbe) Recheck(context.Context) error {
	*f.events = append(*f.events, "recheck")
	return nil
}
func (f *fakeServiceProbe) Close(context.Context) error {
	*f.events = append(*f.events, "scope-close")
	return nil
}

type fakeBootstrapPair struct {
	events  *[]string
	record  []byte
	writes  int
	failure error
}

func (*fakeBootstrapPair) ChildReadHandle() uintptr { return 30 }
func (f *fakeBootstrapPair) Publish(_ context.Context, b []byte) error {
	*f.events = append(*f.events, "write")
	f.writes++
	f.record = append([]byte(nil), b...)
	return f.failure
}
func (f *fakeBootstrapPair) ReleaseChildRead(context.Context) error {
	*f.events = append(*f.events, "release-read")
	return nil
}
func (f *fakeBootstrapPair) Close(context.Context) error {
	*f.events = append(*f.events, "pipe-close")
	return nil
}

func nativeConfigFixture(t *testing.T) (NativeBootstrapConfig, supervisor.ProvisionedGrant) {
	t.Helper()
	const user = "S-1-5-19"
	const service = "S-1-5-80-1-2-3-4-5"
	acl := []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}
	nodes := func(path string) []protectedkey.NodePolicy {
		return []protectedkey.NodePolicy{{Path: `C:\`, OwnerSID: user, ACL: acl}, {Path: `C:\protected`, OwnerSID: user, ACL: acl}, {Path: path, OwnerSID: user, ACL: acl}}
	}
	volume := protectedkey.VolumeProfile{Root: `C:\`, Filesystem: "NTFS", Serial: 7}
	// 公開RFC 8032の公開鍵のみ。秘密鍵生成・読取りは行わない。
	pub, _ := hex.DecodeString("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	key, err := ssh.NewPublicKey(ed25519.PublicKey(pub))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{key})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := backend.PolicyDigest("restricted")
	if err != nil {
		t.Fatal(err)
	}
	keys := protectedkey.Profile{Version: 1, ProcessUserSID: user, ServiceSID: service, OperatorSID: "S-1-5-21-1-2-3-1001", GuestSID: "S-1-5-21-1-2-3-1002", Volume: volume, Provisioning: protectedkey.ProvisioningRecord{Reference: "synthetic_record", ProtectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Catalog: map[string]protectedkey.KeyProfile{"host_key": {Path: `C:\protected\host-key`, Fingerprint: ssh.FingerprintSHA256(key), Nodes: nodes(`C:\protected\host-key`)}}}
	image := func(path string) bootstrapauth.Image {
		return bootstrapauth.Image{Path: path, SHA256: [32]byte{1}, MaxBytes: 1024 * 1024, Volume: volume, Nodes: nodes(path)}
	}
	cfg := NativeBootstrapConfig{Profile: bootstrapauth.Profile{Keys: keys, ServiceName: "RescueHostSupervisor", SupervisorImage: image(`C:\protected\rescue-supervisor.exe`), ChildImage: image(`C:\protected\rescue-backend.exe`)}, PrimaryToken: 20, HostKeyRef: "host_key", Backend: bootstrap.Config{OperationMode: "restricted", AllowedKeys: []string{base64.StdEncoding.EncodeToString(key.Marshal())}, MaximumDuration: 20 * time.Second, LeaseDuration: time.Second, MaxChannels: 1, MaxHandshakes: 2, MaxTransports: 3}, Limits: stdio.Limits{FrameBytes: 16384, Frames: 10, CleanupTimeout: time.Second}}
	g := supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: 1, Identity: supervisor.Identity{LaunchID: "native_1", Epoch: "1", PolicyDigest: policy}}, KeySetDigest: digest, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: time.Now().Add(time.Minute).Round(0), BackendLeaseCeiling: 2 * time.Second}
	return cfg, g
}

func nativeSourceFixture(t *testing.T) (*nativeChildProvisioning, *supervisor.Authority, IssuedLaunch, *fakeBootstrapPair, *[]string) {
	t.Helper()
	cfg, g := nativeConfigFixture(t)
	a, err := supervisor.New(g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Revoke(g.Binding) })
	events := []string{}
	base := nativeBaseFunc(func(_ context.Context, g supervisor.ProvisionedGrant) (ChildPlan, error) {
		events = append(events, "base")
		wire := control.WireContext{Generation: strconv.FormatUint(g.Binding.Generation, 10), Identity: control.WireIdentity{LaunchID: g.Binding.Identity.LaunchID, Epoch: g.Binding.Identity.Epoch, PolicyDigest: g.Binding.Identity.PolicyDigest}, Binding: control.Binding{ArtifactSHA256: hex.EncodeToString(cfg.Profile.ChildImage.SHA256[:]), ChannelNonce: strings.Repeat("b", 64)}, PolicyRef: "approved"}
		ctrl, err := NewOwnedControl(OwnedControlConfig{Wire: wire, Input: &nativeInput{}, Output: io.NopCloser(strings.NewReader("")), PollInterval: time.Millisecond, OperationTimeout: time.Second})
		return ChildPlan{Spec: childjob.Spec{Artifact: childjob.Artifact{Path: cfg.Profile.ChildImage.Path, SHA256: cfg.Profile.ChildImage.SHA256}, Handles: childjob.Handles{Stdin: 10, Stdout: 11, Stderr: 12}, PrimaryToken: cfg.PrimaryToken, CleanupTimeout: time.Second}, Control: ctrl}, err
	})
	raw, err := NewNativeChildProvisioning(base, cfg)
	if err != nil {
		t.Fatal(err)
	}
	n := raw.(*nativeChildProvisioning)
	pair := &fakeBootstrapPair{events: &events}
	n.api = nativeBootstrapCalls{verify: func(_ context.Context, h uintptr) (serviceProbe, error) {
		events = append(events, "verify")
		if h != 20 {
			t.Fatal("別primary token")
		}
		return &fakeServiceProbe{events: &events}, nil
	}, pair: func(context.Context, bootstrapio.Setup) (bootstrapPair, error) {
		events = append(events, "pair")
		return pair, nil
	}, closeChild: func(_ context.Context, h *childjob.Handles, _ uintptr) error {
		events = append(events, "close-child")
		*h = childjob.Handles{}
		return nil
	}}
	return n, a, IssuedLaunch{instance: [32]byte{3}, binding: g.Binding}, pair, &events
}

func TestNativePublicationCleanupContinuesAfterChildHandleFailure(t *testing.T) {
	n, a, i, _, events := nativeSourceFixture(t)
	n.api.closeChild = func(context.Context, *childjob.Handles, uintptr) error {
		*events = append(*events, "child-close-failed")
		return ErrRuntimeUnconfirmed
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	plan, err := n.prepareIssued(ctx, a, i)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.PreparedBootstrap.Close(ctx); !errors.Is(err, ErrRuntimeUnconfirmed) {
		t.Fatal("未確認の回収を成功にした")
	}
	joined := strings.Join(*events, ",")
	if !strings.Contains(joined, "pipe-close") || !strings.Contains(joined, "scope-close") {
		t.Fatalf("独立した所有物の回収を打ち切った: %v", *events)
	}
	if a.Snapshot(time.Now()).State != supervisor.Revoked {
		t.Fatal("回収前に同じAuthorityを失効していない")
	}
}

func TestNativeProvisionerConnectsSameAuthorityRecordToOwnedPipe(t *testing.T) {
	n, a, i, pair, events := nativeSourceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	plan, err := n.prepareIssued(ctx, a, i)
	if err != nil || plan.PreparedBootstrap == nil || plan.Spec.Bootstrap == nil || plan.Bootstrap != nil {
		t.Fatalf("具体的native経路を準備していない: %v", err)
	}
	if plan.Spec.Bootstrap.ReadHandle != 30 || plan.Spec.Bootstrap.Values.Wire != plan.Control.config.Wire {
		t.Fatal("第4readと制御bindingが一致しない")
	}
	if err := plan.PreparedBootstrap.Publish(ctx, a); err != nil {
		t.Fatal(err)
	}
	g := a.Snapshot(time.Now()).Grant
	e := bootstrap.Expected{IssuerInstanceEpoch: hex.EncodeToString(i.instance[:]), Grant: g, Wire: plan.Control.config.Wire, Config: n.cfg.Backend, HostKeyRef: n.cfg.HostKeyRef, Limits: n.cfg.Limits}
	m, err := bootstrap.DecodeRecord(pair.record, e, bootstrap.ReceiverLimits{MaxFrames: 10, MaxCleanupTimeout: time.Second})
	if err != nil || !m.Grant.HardDeadline.Equal(g.HardDeadline) || m.Grant.Binding != i.Binding() {
		t.Fatalf("別grantまたは期限再基準化: %v", err)
	}
	if pair.writes != 1 || strings.Join(*events, ",") != "verify,base,pair,recheck,close-child,release-read,write,recheck,scope-close" {
		t.Fatalf("認証/所有解放/送信順: %v", *events)
	}
	if err := plan.PreparedBootstrap.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNativePublicationFailureBurnsAndRevokesWithoutResend(t *testing.T) {
	n, a, i, pair, _ := nativeSourceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	plan, err := n.prepareIssued(ctx, a, i)
	if err != nil {
		t.Fatal(err)
	}
	pair.failure = ErrRuntimeUnconfirmed
	if err := plan.PreparedBootstrap.Publish(ctx, a); err == nil {
		t.Fatal("不確定な書込を成功にした")
	}
	if a.Snapshot(time.Now()).State != supervisor.Revoked {
		t.Fatal("書込失敗で元のAuthorityを失効していない")
	}
	if err := plan.PreparedBootstrap.Publish(ctx, a); !errors.Is(err, ErrAlreadyUsed) || pair.writes != 1 {
		t.Fatal("同じgrantの送信を再試行した")
	}
	if _, err := n.prepareIssued(ctx, a, i); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("同じprovisionerを再利用した")
	}
	if err := plan.PreparedBootstrap.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNativePublicationRejectsDifferentAuthorityPointerAndCanceledSend(t *testing.T) {
	for _, mode := range []string{"clone", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			n, a, i, pair, _ := nativeSourceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			plan, err := n.prepareIssued(ctx, a, i)
			if err != nil {
				t.Fatal(err)
			}
			other := a
			if mode == "clone" {
				other, err = supervisor.New(a.Snapshot(time.Now()).Grant, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				defer other.Revoke(i.binding)
			} else {
				cancel()
			}
			if err := plan.PreparedBootstrap.Publish(ctx, other); err == nil || pair.writes != 0 {
				t.Fatal("別Authorityまたは取消済みの送信を許可")
			}
			if a.Snapshot(time.Now()).State != supervisor.Revoked {
				t.Fatal("消費済みの元grantが残った")
			}
			cleanup, end := context.WithTimeout(context.Background(), time.Second)
			defer end()
			if err := plan.PreparedBootstrap.Close(cleanup); err != nil {
				t.Fatal(err)
			}
		})
	}
}
