package serviceprovision

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestRuntimeGuestSourceUsesSeparateProtectedPair(t *testing.T) {
	a := rootAnchor()
	r, err := runtimeGuestAnchor(a)
	if err != nil || r.RecordPath != `C:\safe\guest-runtime-v1.json` || r.DigestPath != `C:\safe\guest-runtime-v1.sha256` {
		t.Fatal("runtime source must use its own protected pair", err)
	}
	old, err := guestAnchor(a)
	if err != nil || old == r {
		t.Fatal("legacy source was reused for runtime", err)
	}
	for _, path := range []string{r.RecordPath, r.DigestPath} {
		bad := a
		bad.RecordPath = path
		if _, err = runtimeGuestAnchor(bad); err == nil {
			t.Fatal("runtime source overlaps the base source")
		}
	}
}

func TestRuntimeGuestSourceRejectsCanceledBeforeNativeOpen(t *testing.T) {
	p, _, _, _ := guestConfigurationFixture(t)
	ctx, cancel := context.WithCancel(rootContext(t))
	cancel()
	if g, err := p.LoadRuntimeGuestConfiguration(ctx); err == nil || g != nil {
		t.Fatal("canceled runtime source opened a native owner", err)
	}
}

func runtimeConfigurationFixture(t *testing.T) (*Prepared, *Root, *rootFakeKernel, []byte) {
	t.Helper()
	p, old, k, data := guestConfigurationFixture(t)
	// Model the adopted runtime's distinct SYSTEM service subject; legacy
	// fixtures intentionally retain their original subject unchanged.
	p.loaded.record = []byte(strings.ReplaceAll(string(p.loaded.record), rootUserSID, "S-1-5-18"))
	p.loaded.identity.user, p.loaded.subject.ProcessUserSID = "S-1-5-18", "S-1-5-18"
	p.loaded.api.(*subjectFaultKernel).rootFakeKernel.identity.user = "S-1-5-18"
	k.identity.user = "S-1-5-18"
	data = []byte(strings.ReplaceAll(string(data), rootUserSID, "S-1-5-18"))
	var decodeErr error
	p.record, decodeErr = DecodeRecord(p.loaded.record)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	a, err := runtimeGuestAnchor(p.loaded.anchor)
	if err != nil {
		t.Fatal(err)
	}
	k.files[a.RecordPath], k.files[a.DigestPath] = k.files[old.anchor.RecordPath], k.files[old.anchor.DigestPath]
	delete(k.files, old.anchor.RecordPath)
	delete(k.files, old.anchor.DigestPath)
	k.files[a.RecordPath].state.path = a.RecordPath
	k.files[a.DigestPath].state.path = a.DigestPath
	s := strings.ReplaceAll(string(data), "guest-provisioning-v1.", "guest-runtime-v1.")
	s = strings.Replace(s, `"protocol":"rescue-guest-provisioning/1"`, `"protocol":"rescue-host-guest-runtime/1","logonProfile":"local-batch/1"`, 1)
	s = strings.ReplaceAll(s, `"S-1-5-2"`, `"S-1-5-3"`)
	s = strings.ReplaceAll(s, "SeNetworkLogonRight", "SeBatchLogonRight")
	data = []byte(s)
	setGuestFixtureData(k, a, data)
	r, err := NewNativeRoot(a)
	if err != nil {
		t.Fatal(err)
	}
	r.api = k
	return p, r, k, data
}

func TestRuntimeGuestConfigurationBindsSeparateBytesAndRejectsLegacySource(t *testing.T) {
	p, r, _, data := runtimeConfigurationFixture(t)
	if g, err := p.loadFixedGuest(rootContext(t), r); err == nil || g != nil {
		t.Fatal("runtime pair accepted by legacy entry", err)
	}
	g, err := p.loadRuntimeGuestConfiguration(rootContext(t), r)
	if err != nil || g == nil {
		t.Fatal("protected runtime pair not resolved", err)
	}
	l, err := g.Layout(rootContext(t))
	if err != nil || l.Digest() != sha256.Sum256(data) || !g.runtimeGuest {
		t.Fatal("runtime bytes were not retained", err)
	}
	actual, err := g.Digest(rootContext(t))
	buf := append([]byte("rescue-runtime-guest-bundle/1\x00"), g.baseDigest[:]...)
	other := l.Digest()
	buf = append(buf, other[:]...)
	if err != nil || actual != sha256.Sum256(buf) {
		t.Fatal("runtime confirmation digest lost its domain", err)
	}
	if err = g.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err = g.Layout(rootContext(t)); err == nil {
		t.Fatal("closed runtime source revived")
	}
}

func TestRuntimeGuestConfigurationRejectsLegacyOrForeignProtectedContents(t *testing.T) {
	for _, kind := range []string{"network", "subject", "file", "digest"} {
		t.Run(kind, func(t *testing.T) {
			p, r, k, data := runtimeConfigurationFixture(t)
			switch kind {
			case "network":
				data = []byte(strings.ReplaceAll(string(data), "local-batch/1", "network/1"))
			case "subject":
				data = []byte(strings.ReplaceAll(string(data), p.record.Profile.Keys.OperatorSID, "S-1-5-21-1-2-3-9001"))
			case "file":
				data = []byte(strings.ReplaceAll(string(data), `03000000000000000000000000000000`, `33000000000000000000000000000000`))
			}
			setGuestFixtureData(k, r.anchor, data)
			if kind == "digest" {
				k.files[r.anchor.DigestPath].data[0] = 'z'
			}
			g, err := p.loadRuntimeGuestConfiguration(rootContext(t), r)
			if err == nil {
				t.Fatal("foreign runtime contents accepted")
			}
			if g != nil {
				if err = g.Close(rootContext(t)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRuntimeNativeFactoryHoldsRuntimeBundleBeforeStrongAcquisition(t *testing.T) {
	p, r, _, _ := runtimeConfigurationFixture(t)
	e, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil || e == nil || !e.runtimeGuest || e.staging {
		t.Fatal("explicit runtime mode was not isolated", err)
	}
	strongCalls := 0
	s := &nativeExecutionStage{cfg: e.cfg, preview: e.preview, runtimeGuest: true, requestBudgets: runtimeRequestBudgets(), calls: nativeExecutionCalls{
		prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil },
		guest: func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) {
			return p.loadRuntimeGuestConfiguration(ctx, r)
		},
		launch: func(context.Context, *Prepared) (*LaunchToken, error) { strongCalls++; return nil, ErrLaunchDenied },
	}}
	service, err := s.Initialize(rootContext(t))
	if err != nil || service == nil || s.guest == nil || s.previewSnapshot == nil || strongCalls != 0 {
		t.Fatal("runtime confirmation did not hold the fixed pair first", err)
	}
	digest, err := s.guest.Digest(rootContext(t))
	if err != nil || s.previewSnapshot.ConfigDigest != digest {
		t.Fatal("runtime layout was outside the confirmed bundle", err)
	}
	if err = s.VerifyPreviewConfirmation(rootContext(t), *s.previewSnapshot); err != nil {
		t.Fatal("confirmed runtime bytes could not be rechecked", err)
	}
	s.runtimeAttempt = &nativeRuntimeAttempt{}
	if err = s.Close(rootContext(t)); err == nil || s.guest == nil || s.prepared == nil {
		t.Fatal("outer source released before runtime guest close acknowledgement")
	}
	s.runtimeAttempt.closed = true // memory model of the exact runtime owner ACK
	if err = service.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeNativeFactoryRejectsLegacyBundleBeforeStrongAcquisition(t *testing.T) {
	p, r, _, _ := guestConfigurationFixture(t)
	limits := previewExecutionConfig().Limits
	strongCalls := 0
	s := &nativeExecutionStage{cfg: ExecutionConfig{}, preview: &limits, runtimeGuest: true, requestBudgets: runtimeRequestBudgets(), calls: nativeExecutionCalls{
		prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil },
		guest:   func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) { return p.loadFixedGuest(ctx, r) },
		launch:  func(context.Context, *Prepared) (*LaunchToken, error) { strongCalls++; return nil, ErrLaunchDenied },
	}}
	if service, err := s.Initialize(rootContext(t)); err == nil || service != nil || strongCalls != 0 {
		t.Fatal("legacy guest bundle reached runtime acquisition", err)
	}
	if err := s.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
}
