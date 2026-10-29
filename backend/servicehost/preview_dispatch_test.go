package servicehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// The public constructor is real and contains no legacy provider. Only the
// existing private SCM/auth operation seams are modeled; no native proof exists.
func previewAuthFixture(t *testing.T, c PreviewServiceConfig) (*Service, *serviceAuthAdapter) {
	t.Helper()
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Budgets.Observe = time.Hour // model-only periodic interference suppression
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	go func() { _ = s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for s.Snapshot().State != Running {
		select {
		case <-deadline.C:
			t.Fatal("preview fixture did not become ready")
		case <-time.After(time.Millisecond):
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, s.auth
}
func previewExchange(t *testing.T, a *serviceAuthAdapter, frame string) servicecontrol.UntrustedResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var response servicecontrol.UntrustedResponse
	if err := a.exchangeOps(ctx, fixtureAuthBatch(a, frame), func(_ context.Context, bytes []byte) error {
		var err error
		response, err = servicecontrol.DecodeUntrustedResponseFrame(bytes)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return response
}
func TestPreviewPrepareUsesSharedCanonicalDescriptorWithoutGrantOrLegacyLaunch(t *testing.T) {
	c := previewConfigFixture(t)
	s, a := previewAuthFixture(t, c)
	hello := previewExchange(t, a, dispatchHello)
	before := time.Now()
	response := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	after := time.Now()
	if response.Result != "intent-prepared" || response.Intent == nil {
		t.Fatal("authenticated prepare did not produce display intent", response)
	}
	intent := response.Intent
	if !dispatchHex(intent.IntentID, 32) || intent.Preview.BootEpoch != hello.BootEpoch {
		t.Fatal("intent lost fresh ID or actual boot")
	}
	expires := time.UnixMilli(intent.IntentExpiresUnixMs)
	if expires.Before(before.Add(c.IntentTTL).Truncate(time.Millisecond)) || expires.After(after.Add(c.IntentTTL)) || expires.After(c.Preview.HardDeadline) {
		t.Fatal("intent expiry exceeded its explicit bounds", expires)
	}
	// Expected bytes are rooted in the shared resolver fixture, not recomputed
	// from the response's descriptor fields. IDs and boot are runtime correlations.
	want := fmt.Sprintf(`{"bootEpoch":%q,"intentId":%q,"configDigest":%q,"policyDigest":%q,"keySetDigest":%q,"allowedKeyFingerprints":[%q],"target":{"host":"127.0.0.1","port":"22","outerChannel":"direct-tcpip"},"permissionSummary":{"policyMode":"restricted","guestClass":"nonadmin","outerChannels":["direct-tcpip"]},"hardDeadlineUnixMs":%q}`, hello.BootEpoch, intent.IntentID, hex.EncodeToString(c.Preview.ConfigDigest[:]), hex.EncodeToString(c.Preview.PolicyDigest[:]), hex.EncodeToString(c.Preview.KeySetDigest[:]), c.Preview.AllowedKeyFingerprints[0], fmt.Sprint(c.Preview.HardDeadline.UnixMilli()))
	canonical, err := servicecontrol.CanonicalPreview(intent.Preview)
	if err != nil || string(canonical) != want {
		t.Fatal("shared descriptor canonical preview mismatch", err)
	}
	digest := sha256.Sum256(append([]byte("rescue-host-service/1:preview\x00"), []byte(want)...))
	if intent.PreviewDigest != hex.EncodeToString(digest[:]) {
		t.Fatal("preview digest did not bind canonical candidate")
	}
	confirm := fmt.Sprintf(`{"protocol":"rescue-host-service/1","type":"request","bootEpoch":%q,"channelNonce":%q,"sequence":"2","requestId":"33333333333333333333333333333333","command":"confirm-approval","body":{"intentId":%q,"previewDigest":%q}}`+"\n", hello.BootEpoch, hello.ChannelNonce, intent.IntentID, intent.PreviewDigest)
	denied := previewExchange(t, a, confirm)
	if denied.Error == nil || denied.Error.Code != "SERVICE_UNCONFIGURED" || denied.Approval != nil {
		t.Fatal("display intent became approval")
	}
	if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority || s.cfg.Provisioning != nil || s.cfg.Launcher != nil || s.ownerIntent != nil || s.reservation != nil || s.host != nil || s.delivery != nil {
		t.Fatal("preview touched legacy approval, workload reservation, or authority")
	}
}

func TestPreviewReprepareStopReplayAndWriteFailureInvalidatePrivateCandidates(t *testing.T) {
	for _, kind := range []string{"reprepare", "stop", "replay", "write-failure"} {
		t.Run(kind, func(t *testing.T) {
			c := previewConfigFixture(t)
			s, a := previewAuthFixture(t, c)
			hello := previewExchange(t, a, dispatchHello)
			preparedFrame := dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent)
			first := previewExchange(t, a, preparedFrame)
			if first.Intent == nil {
				t.Fatal("first display intent missing")
			}
			s.mu.Lock()
			previous := s.preview.intent
			s.mu.Unlock()
			switch kind {
			case "reprepare":
				second := previewExchange(t, a, dispatchRequest(hello, 2, "33333333333333333333333333333333", servicecontrol.PrepareIntent))
				if second.Intent == nil || second.Intent.IntentID == first.Intent.IntentID || second.Intent.Preview.HardDeadlineUnixMs != first.Intent.Preview.HardDeadlineUnixMs {
					t.Fatal("reprepare reused ID or extended fixed descriptor deadline")
				}
			case "stop":
				s.requestStop()
			case "replay", "write-failure":
				frame := preparedFrame
				if kind == "write-failure" {
					frame = dispatchRequest(hello, 2, "33333333333333333333333333333333", servicecontrol.PrepareIntent)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := a.exchangeOps(ctx, fixtureAuthBatch(a, frame), func(context.Context, []byte) error { return fmt.Errorf("model write failed") }); err == nil {
					t.Fatal("failed/replayed exchange succeeded")
				}
			}
			s.mu.Lock()
			oldInvalid := previous.invalidated
			current := s.preview.intent
			currentInvalid := current != nil && current.invalidated
			unchangedWorkload := s.ownerIntent == nil && s.reservation == nil
			s.mu.Unlock()
			if !oldInvalid {
				t.Fatal("previous display intent stayed live", kind)
			}
			if kind != "reprepare" && !currentInvalid {
				t.Fatal("stopped/failed channel retained a live preview")
			}
			if !unchangedWorkload || s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
				t.Fatal("preview invalidation touched workload authority")
			}
		})
	}
}

func TestPreviewIntentExpiryCapsTTLAtFixedDescriptorDeadline(t *testing.T) {
	c := previewConfigFixture(t)
	c.IntentTTL = time.Hour
	s, a := previewAuthFixture(t, c)
	_ = previewExchange(t, a, dispatchHello)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.mu.Lock()
	first, code := s.preparePreviewIntentLocked(ctx, s.dispatch, "22222222222222222222222222222222", nil, c.Preview.HardDeadline.Add(-time.Second))
	old := s.preview.intent
	expired, expiredCode := s.preparePreviewIntentLocked(ctx, s.dispatch, "33333333333333333333333333333333", nil, c.Preview.HardDeadline.Add(time.Nanosecond))
	fixed := s.preview.descriptor.HardDeadline
	invalid := old != nil && old.invalidated
	s.mu.Unlock()
	if code != "" || first == nil || first.IntentExpiresUnixMs != c.Preview.HardDeadline.UnixMilli() {
		t.Fatal("TTL was not capped at the fixed descriptor deadline", code)
	}
	if expired != nil || expiredCode != "SERVICE_INTENT_EXPIRED" {
		t.Fatal("reprepare renewed expired descriptor", expiredCode)
	}
	if !fixed.Equal(c.Preview.HardDeadline) || !invalid || s.Snapshot().Generation != 0 {
		t.Fatal("expired display state extended deadline or remained current")
	}
}

func TestPreviewStopWinsBeforeLateAuthenticatedConsumeCanPrepare(t *testing.T) {
	c := previewConfigFixture(t)
	s, a := previewAuthFixture(t, c)
	hello := previewExchange(t, a, dispatchHello)
	_ = previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	s.mu.Lock()
	old := s.preview.intent
	s.mu.Unlock()
	frame := dispatchRequest(hello, 2, "33333333333333333333333333333333", servicecontrol.PrepareIntent)
	batch := fixtureAuthBatch(a, frame)
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch.consume = func(c context.Context, _ *operatorauth.Read) ([]byte, error) {
		close(entered)
		select {
		case <-release:
		case <-c.Done():
		}
		return []byte(frame), nil
	}
	var writes atomic.Int32
	result := make(chan error, 1)
	go func() {
		result <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { writes.Add(1); return nil })
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("authenticated consumer did not enter")
	}
	s.requestStop()
	close(release)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("late consume prepared after stop")
		}
	case <-ctx.Done():
		t.Fatal("late worker did not join")
	}
	if intent, code := s.preparePreviewIntent(ctx, s.dispatch); intent != nil || code != "SERVICE_STOPPING" {
		t.Fatal("stop gate accepted another preview", code)
	}
	s.mu.Lock()
	same := s.preview.intent == old && old.invalidated && len(s.preview.seen) == 1
	s.mu.Unlock()
	if !same || writes.Load() != 0 || s.Snapshot().Generation != 0 {
		t.Fatal("stop lost preview ordering")
	}
}

func TestPreviewExplicitAuthenticatedChannelDisconnectInvalidatesCandidate(t *testing.T) {
	c := previewConfigFixture(t)
	s, a := previewAuthFixture(t, c)
	hello := previewExchange(t, a, dispatchHello)
	_ = previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch := fixtureAuthBatch(a, dispatchRequest(hello, 2, "33333333333333333333333333333333", servicecontrol.Status))
	batch.collect = func(context.Context) (*operatorauth.Read, error) { return nil, io.EOF }
	if err := a.exchangeOps(ctx, batch, func(context.Context, []byte) error { t.Error("disconnected channel wrote response"); return nil }); err == nil {
		t.Fatal("disconnected authentication owner succeeded")
	}
	s.mu.Lock()
	invalid := s.preview.intent.invalidated
	s.mu.Unlock()
	if !invalid || !s.issuer.Snapshot().Closed || s.Snapshot().Generation != 0 {
		t.Fatal("channel loss left preview current or issued grant")
	}
}
