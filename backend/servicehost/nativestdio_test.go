package servicehost

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

type stdioReleaseFixture struct {
	drainPipeFixture
	released int
}

func (p *stdioReleaseFixture) ReleaseChild(context.Context) error { p.released++; return nil }

func TestNativeStdioPlanUsesCurrentGrantAndOneOwnerForChildCopies(t *testing.T) {
	n, a, issued, _, events := nativeSourceFixture(t)
	c := NativeStdioConfig{Profile: n.cfg.Profile, PrimaryToken: n.cfg.PrimaryToken, PolicyRef: "approved", PollInterval: time.Millisecond, OperationTimeout: time.Second, CleanupTimeout: time.Second, StderrBytes: 32}
	raw, err := NewNativeStdioProvisioning(c)
	if err != nil {
		t.Fatal(err)
	}
	base := raw.(*nativeStdioProvisioning)
	pipes := &stdioReleaseFixture{drainPipeFixture: drainPipeFixture{stderr: io.NopCloser(strings.NewReader("synthetic"))}}
	base.open = func(ctx context.Context, s childstdio.Setup) (stdioPipes, error) {
		if s.UserSID != c.Profile.Keys.ProcessUserSID || s.ServiceSID != c.Profile.Keys.ServiceSID || s.CleanupTimeout != c.CleanupTimeout {
			t.Fatal("stdio profile changed")
		}
		*events = append(*events, "stdio-open")
		return pipes, nil
	}
	n.base = base
	n.api.closeChild = func(context.Context, *childjob.Handles, uintptr) error {
		t.Fatal("raw handle copy closed outside stdio owner")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	plan, err := n.prepareIssued(ctx, a, issued)
	if err != nil {
		t.Fatalf("native stdio plan not connected: %v", err)
	}
	defer plan.PreparedBootstrap.Close(ctx)
	if plan.Stdio == nil || plan.Control == nil || plan.Spec.Handles != pipes.ChildHandles() || plan.Spec.PrimaryToken != c.PrimaryToken || plan.Spec.Artifact.Path != c.Profile.ChildImage.Path || plan.Spec.Artifact.SHA256 != c.Profile.ChildImage.SHA256 {
		t.Fatal("stdio ownership or fixed artifact not bound")
	}
	if plan.Control.config.Wire.Generation != "1" || plan.Control.config.Wire.Identity.LaunchID != issued.binding.Identity.LaunchID || plan.Control.config.Wire.Binding.ChannelNonce == "" {
		t.Fatal("wire not derived from current grant")
	}
	if err := plan.PreparedBootstrap.Publish(ctx, a); err != nil {
		t.Fatal(err)
	}
	if pipes.released != 1 {
		t.Fatalf("child copies not released once: %d", pipes.released)
	}
}

type stdioReadCounter struct{ reads atomic.Int32 }

func (p *stdioReadCounter) Read([]byte) (int, error) { p.reads.Add(1); return 0, io.EOF }
func (*stdioReadCounter) Close() error               { return nil }

func TestPartialNativeStdioAllocationNeverStartsADrain(t *testing.T) {
	n, a, issued, _, _ := nativeSourceFixture(t)
	raw, err := NewNativeStdioProvisioning(NativeStdioConfig{Profile: n.cfg.Profile, PrimaryToken: n.cfg.PrimaryToken, PolicyRef: "approved", PollInterval: time.Millisecond, OperationTimeout: time.Second, CleanupTimeout: time.Second, StderrBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	base := raw.(*nativeStdioProvisioning)
	counter := &stdioReadCounter{}
	pipes := &drainPipeFixture{stderr: counter}
	base.open = func(context.Context, childstdio.Setup) (stdioPipes, error) {
		return pipes, errors.New("synthetic partial native allocation")
	}
	n.base = base
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	plan, err := n.prepareIssued(ctx, a, issued)
	if err == nil || plan.Stdio == nil || plan.PreparedBootstrap == nil {
		t.Fatal("partial stdio owner was discarded")
	}
	if err := plan.PreparedBootstrap.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if counter.reads.Load() != 0 {
		t.Fatal("failed factory started stderr I/O")
	}
}

// 実Session/stdioをmemory pipeへ接続し、OS processとhandleだけを差し替える。
type stdioRuntimePipes struct {
	input          io.WriteCloser
	output, stderr io.ReadCloser
	releases       int
}

func (*stdioRuntimePipes) ChildHandles() childjob.Handles {
	return childjob.Handles{Stdin: 10, Stdout: 11, Stderr: 12}
}
func (p *stdioRuntimePipes) Input() io.WriteCloser              { return p.input }
func (p *stdioRuntimePipes) Output() io.ReadCloser              { return p.output }
func (p *stdioRuntimePipes) Stderr() io.ReadCloser              { return p.stderr }
func (p *stdioRuntimePipes) ReleaseChild(context.Context) error { p.releases++; return nil }
func (p *stdioRuntimePipes) Close(context.Context) error {
	return errors.Join(p.input.Close(), p.output.Close(), p.stderr.Close())
}

type stdioSyntheticProcess struct{ wait, close func(context.Context) error }

func (p *stdioSyntheticProcess) Wait(c context.Context) error  { return p.wait(c) }
func (p *stdioSyntheticProcess) Close(c context.Context) error { return p.close(c) }

type stdioPublicSigner struct{ key ssh.PublicKey }

func (s stdioPublicSigner) PublicKey() ssh.PublicKey { return s.key }
func (stdioPublicSigner) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	return nil, errors.New("合成公開鍵は署名しない")
}

func TestNativeStdioRuntimeRequiresStderrEOFAfterRealGoCloseAndExit(t *testing.T) {
	t.Run("eof", func(t *testing.T) { nativeStdioRuntimeLifecycle(t, false, false) })
}
func TestStderrFailureDoesNotSkipBootstrapCleanup(t *testing.T) {
	nativeStdioRuntimeLifecycle(t, true, false)
}
func TestMissingStderrEOFRetainsTimeForPhysicalCleanup(t *testing.T) {
	nativeStdioRuntimeLifecycle(t, false, true)
}
func nativeStdioRuntimeLifecycle(t *testing.T, failStderr, keepStderrOpen bool) {
	t.Helper()
	n, a, issued, _, events := nativeSourceFixture(t)
	cfg := n.cfg
	raw, err := NewNativeStdioProvisioning(NativeStdioConfig{Profile: cfg.Profile, PrimaryToken: cfg.PrimaryToken, PolicyRef: "approved", PollInterval: time.Millisecond, OperationTimeout: time.Second, CleanupTimeout: time.Second, StderrBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	base := raw.(*nativeStdioProvisioning)
	childInput, ownerInput := io.Pipe()
	ownerOutput, childOutput := io.Pipe()
	ownerError, childError := io.Pipe()
	for _, stream := range []io.Closer{childInput, ownerInput, ownerOutput, childOutput, ownerError, childError} {
		defer stream.Close()
	}
	pipes := &stdioRuntimePipes{input: ownerInput, output: ownerOutput, stderr: ownerError}
	base.open = func(context.Context, childstdio.Setup) (stdioPipes, error) { return pipes, nil }
	n.base = base
	observed := make(chan struct{}, 1)
	backendDone := make(chan error, 1)
	forced := 0
	runtime := NewJobRuntime(n)
	runtime.startChild = func(_ context.Context, spec childjob.Spec) (Process, error) {
		data, _ := base64.StdEncoding.DecodeString(cfg.Backend.AllowedKeys[0])
		key, e := ssh.ParsePublicKey(data)
		if e != nil {
			return nil, e
		}
		grant := a.Snapshot(time.Now()).Grant
		actual, e := runbackend.New(runbackend.ApprovedConfig{Grant: grant, Backend: backend.Config{Identity: grant.Binding.Identity, OperationMode: cfg.Backend.OperationMode, AllowedKeys: []ssh.PublicKey{key}, HostKey: stdioPublicSigner{key}, MaximumDuration: cfg.Backend.MaximumDuration, LeaseDuration: cfg.Backend.LeaseDuration, MaxChannels: cfg.Backend.MaxChannels, MaxHandshakes: cfg.Backend.MaxHandshakes, MaxTransports: cfg.Backend.MaxTransports}, Wire: spec.Bootstrap.Values.Wire, Limits: cfg.Limits})
		if e != nil {
			return nil, e
		}
		go func() { backendDone <- actual.Run(context.Background(), childInput, childOutput) }()
		return &stdioSyntheticProcess{wait: func(ctx context.Context) error {
			select {
			case e := <-backendDone:
				observed <- struct{}{}
				return e
			case <-ctx.Done():
				return ctx.Err()
			}
		}, close: func(context.Context) error {
			forced++
			return errors.Join(childInput.Close(), childOutput.Close(), childError.Close())
		}}, nil
	}
	budget := 3 * time.Second
	if keepStderrOpen {
		budget = 300 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	process, err := runtime.StartIssued(ctx, a, issued)
	if err != nil {
		if process != nil {
			_ = process.Close(ctx)
		}
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- process.Close(ctx) }()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("real Go close/exit not observed")
	}
	select {
	case e := <-closed:
		t.Fatalf("stderr EOF was not awaited: %v", e)
	case <-time.After(20 * time.Millisecond):
	}
	if keepStderrOpen {
		// 子のstderr writerは保持し、親の有限回収だけを観察する。
	} else if failStderr {
		_ = childError.CloseWithError(errors.New("synthetic stderr failure"))
	} else if err := childError.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-closed:
		if failStderr || keepStderrOpen {
			if !errors.Is(e, ErrRuntimeUnconfirmed) {
				t.Fatalf("stderr failure was confirmed: %v", e)
			}
		} else if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("stderr EOF did not finish cleanup")
	}
	if !strings.Contains(strings.Join(*events, ","), "pipe-close") {
		t.Fatal("stderr failure skipped bootstrap pipe cleanup")
	}
	if forced != 0 || pipes.releases != 1 {
		t.Fatalf("normal exit forced=%d releases=%d", forced, pipes.releases)
	}
	if a.Snapshot(time.Now()).State != supervisor.Revoked {
		t.Fatal("cleanup retained grant")
	}
}

func TestNativeStdioRejectsUnconfiguredValuesAndLegacyPreparation(t *testing.T) {
	cfg, g := nativeConfigFixture(t)
	valid := NativeStdioConfig{Profile: cfg.Profile, PrimaryToken: cfg.PrimaryToken, PolicyRef: "approved", PollInterval: time.Millisecond, OperationTimeout: time.Second, CleanupTimeout: time.Second, StderrBytes: 32}
	for _, name := range []string{"profile", "token", "reference", "poll", "operation", "cleanup", "stderr"} {
		t.Run(name, func(t *testing.T) {
			c := valid
			switch name {
			case "profile":
				c.Profile = bootstrapauth.Profile{}
			case "token":
				c.PrimaryToken = 0
			case "reference":
				c.PolicyRef = ""
			case "poll":
				c.PollInterval = 0
			case "operation":
				c.OperationTimeout = 0
			case "cleanup":
				c.CleanupTimeout = 0
			case "stderr":
				c.StderrBytes = 0
			}
			if p, err := NewNativeStdioProvisioning(c); err == nil || p != nil {
				t.Fatal("unconfigured stdio accepted")
			}
		})
	}
	source, err := NewNativeStdioProvisioning(valid)
	if err != nil {
		t.Fatal(err)
	}
	source.(*nativeStdioProvisioning).open = func(context.Context, childstdio.Setup) (stdioPipes, error) {
		t.Fatal("legacy route reached native pipe creation")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := source.PrepareChild(ctx, g); !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatalf("legacy base accepted: %v", err)
	}
}

func TestNativeStdioRejectsWrapperIdentityMismatchBeforeNativeCalls(t *testing.T) {
	for _, field := range []string{"user", "service-sid", "service-name", "child-hash", "supervisor-hash", "token"} {
		t.Run(field, func(t *testing.T) {
			wrapper, _ := nativeConfigFixture(t)
			profile := wrapper.Profile
			if field == "user" {
				profile.Keys.ProcessUserSID = "S-1-5-20"
			}
			if field == "service-sid" {
				profile.Keys.ServiceSID = "S-1-5-80-6-7-8-9-10"
			}
			// Profileの意味検査も通る独立した入力を作り、相互bindingの拒否を検査する。
			fixNodes := func(nodes []protectedkey.NodePolicy) []protectedkey.NodePolicy {
				result := append([]protectedkey.NodePolicy(nil), nodes...)
				for i := range result {
					result[i].OwnerSID = profile.Keys.ProcessUserSID
					result[i].ACL = []protectedkey.ACE{{SID: profile.Keys.ServiceSID, Mask: 0x1f01ff}}
				}
				return result
			}
			profile.Keys.Catalog = make(map[string]protectedkey.KeyProfile, len(wrapper.Profile.Keys.Catalog))
			for ref, key := range wrapper.Profile.Keys.Catalog {
				key.Nodes = fixNodes(key.Nodes)
				profile.Keys.Catalog[ref] = key
			}
			profile.ChildImage.Nodes = fixNodes(profile.ChildImage.Nodes)
			profile.SupervisorImage.Nodes = fixNodes(profile.SupervisorImage.Nodes)
			if field == "service-name" {
				profile.ServiceName = "OtherSupervisor"
			}
			if field == "child-hash" {
				profile.ChildImage.SHA256[0]++
			}
			if field == "supervisor-hash" {
				profile.SupervisorImage.SHA256[0]++
			}
			token := wrapper.PrimaryToken
			if field == "token" {
				token++
			}
			raw, err := NewNativeStdioProvisioning(NativeStdioConfig{Profile: profile, PrimaryToken: token, PolicyRef: "approved", PollInterval: time.Millisecond, OperationTimeout: time.Second, CleanupTimeout: time.Second, StderrBytes: 32})
			if err != nil {
				t.Fatalf("independent static profile invalid: %v", err)
			}
			if provisioner, err := NewNativeChildProvisioning(raw, wrapper); err == nil || provisioner != nil {
				t.Fatal("different stdio identity accepted by bootstrap wrapper")
			}
		})
	}
}
