package servicehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceDispatchNoNativeProviderCannotFallbackToLegacy(t *testing.T) {
	d, err := newServiceDispatcher(&Service{}, serviceDispatchProfile{ChannelNonce: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RequestLimit: 4}, nil)
	if err == nil || d != nil {
		t.Fatal("未設定の認証providerがcontrolled経路を作った")
	}
}

type dispatchTestVerifier struct {
	owner  *serviceDispatcher
	fail   bool
	reused *authenticatedReadTicket
}

func (v *dispatchTestVerifier) verify(_ context.Context, frame []byte) (authenticatedServiceRead, error) {
	if v.fail {
		return authenticatedServiceRead{}, ErrProvisioningRejected
	}
	ticket := v.reused
	if ticket == nil {
		ticket = &authenticatedReadTicket{marker: 1}
	}
	return authenticatedServiceRead{owner: v.owner, frame: sha256.Sum256(frame), ticket: ticket}, nil
}
func dispatchTestOwner(t *testing.T, limit uint64) (*Service, *serviceDispatcher, *dispatchTestVerifier, <-chan error, *atomic.Int32) {
	t.Helper()
	var legacy atomic.Int32
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		legacy.Add(1)
		<-ctx.Done()
		return ephemeral.ApprovedPolicy{}, ctx.Err()
	})
	plane := &serviceMemoryPlane{confirm: func(context.Context) error { return nil }}
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	verifier := &dispatchTestVerifier{}
	d, err := newServiceDispatcher(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: limit}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	verifier.owner = d
	done := make(chan error, 1)
	states := make(chan ServiceState, 16)
	go func() { done <- s.Serve(context.Background(), nil, states) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	observed := time.NewTicker(time.Millisecond)
	defer observed.Stop()
	for {
		s.mu.Lock()
		ready := s.state == Running
		s.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-states:
		case <-observed.C:
		case <-deadline.C:
			t.Fatal("controlled owner did not reach actual ready")
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, d, verifier, done, &legacy
}
func dispatchTestFrame(t *testing.T, d *serviceDispatcher, frame string) servicecontrol.UntrustedResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := d.begin(ctx, []byte(frame))
	if err != nil {
		t.Fatal(err)
	}
	response, err := servicecontrol.DecodeUntrustedResponseFrame(reply.frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.complete(reply, nil); err != nil {
		t.Fatal(err)
	}
	return response
}

const dispatchHello = `{"protocol":"rescue-host-service/1","type":"hello","sequence":"0","requestId":"11111111111111111111111111111111","clientNonce":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` + "\n"

func TestServiceDispatchHelloUsesActualOwnerAndNeverReadsLegacyApproval(t *testing.T) {
	s, d, _, _, legacy := dispatchTestOwner(t, 4)
	response := dispatchTestFrame(t, d, dispatchHello)
	owner := s.Snapshot()
	if response.Type != "hello" || response.BootEpoch != hex.EncodeToString(owner.InstanceEpoch[:]) || response.ChannelNonce != strings.Repeat("b", 64) || response.RequestLimit != 4 || response.ClientNonce != strings.Repeat("a", 64) {
		t.Fatal("hello was not bound to the actual Service issuer")
	}
	if legacy.Load() != 0 || owner.Generation != 0 || owner.HasAuthority {
		t.Fatal("controlled path fell back to legacy approval")
	}
}

func dispatchRequest(hello servicecontrol.UntrustedResponse, seq uint64, id string, command servicecontrol.Command) string {
	return fmt.Sprintf(`{"protocol":"rescue-host-service/1","type":"request","bootEpoch":%q,"channelNonce":%q,"sequence":%q,"requestId":%q,"command":%q,"body":{}}`+"\n", hello.BootEpoch, hello.ChannelNonce, strconv.FormatUint(seq, 10), id, command)
}
func TestServiceDispatchStatusIsSameOwnerZeroGrantObservation(t *testing.T) {
	s, d, _, _, legacy := dispatchTestOwner(t, 4)
	hello := dispatchTestFrame(t, d, dispatchHello)
	response := dispatchTestFrame(t, d, dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.Status))
	if response.Result != "snapshot" || response.Sequence != 1 || response.RequestID != strings.Repeat("2", 32) || response.Snapshot == nil {
		t.Fatal("status response lost exact request correlation")
	}
	snapshot := response.Snapshot
	if snapshot.ServicePhase != "running" || snapshot.Generation != 0 || snapshot.ApprovalRequestID != nil || snapshot.Binding != nil || snapshot.LaunchPhase != "none" || snapshot.WorkloadCleanupConfirmed {
		t.Fatal("zero-grant status invented authority/cleanup")
	}
	if legacy.Load() != 0 || s.Snapshot().Generation != 0 {
		t.Fatal("status issued a grant")
	}
}

func TestServiceDispatchQuiesceReceiptRevokesSameIssuerWithoutClaimingWorkloadOrSCMStopped(t *testing.T) {
	s, d, _, done, _ := dispatchTestOwner(t, 4)
	hello := dispatchTestFrame(t, d, dispatchHello)
	response := dispatchTestFrame(t, d, dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.RevokeAllAndQuiesce))
	if response.Result != "quiesce-accepted" || response.Quiesce == nil || !response.Quiesce.RevocationApplied || response.Quiesce.WorkloadCleanupConfirmed {
		t.Fatal("失効受付とworkload/SCM回収を混同した")
	}
	s.mu.Lock()
	issuer := s.issuer
	s.mu.Unlock()
	if !issuer.Snapshot().Closed {
		t.Fatal("同一issuerの実失効より前にreceiptを返した")
	}
	if _, err := issuer.Prepare(lifetimeApproved(t)); err == nil {
		t.Fatal("quiesce後に新たな承認を発行した")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ownerが独立に終了を続行しない")
	}
}

func TestServiceDispatchBindingReplayAndAuthViolationCloseTheChannel(t *testing.T) {
	for _, kind := range []string{"boot", "nonce", "sequence", "hello-id-replay", "read-replay", "foreign-owner", "auth-denied", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			s, d, v, _, _ := dispatchTestOwner(t, 4)
			if kind == "read-replay" {
				v.reused = &authenticatedReadTicket{marker: 1}
			}
			hello := dispatchTestFrame(t, d, dispatchHello)
			frame := dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.Status)
			switch kind {
			case "boot":
				frame = strings.Replace(frame, hello.BootEpoch, strings.Repeat("c", 64), 1)
			case "nonce":
				frame = strings.Replace(frame, hello.ChannelNonce, strings.Repeat("c", 64), 1)
			case "sequence":
				frame = dispatchRequest(hello, 2, strings.Repeat("2", 32), servicecontrol.Status)
			case "hello-id-replay":
				frame = dispatchRequest(hello, 1, strings.Repeat("1", 32), servicecontrol.Status)
			case "foreign-owner":
				v.owner = &serviceDispatcher{}
			case "auth-denied":
				v.fail = true
			case "malformed":
				frame = "{}\n"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := d.begin(ctx, []byte(frame)); err == nil {
				t.Fatal("invalid context/read accepted")
			}
			v.owner = d
			v.fail = false
			v.reused = nil
			if _, err := d.begin(ctx, []byte(dispatchRequest(hello, 1, strings.Repeat("3", 32), servicecontrol.Status))); err == nil {
				t.Fatal("violation did not irrevocably close channel")
			}
			s.mu.Lock()
			issuer := s.issuer
			s.mu.Unlock()
			if !issuer.Snapshot().Closed {
				t.Fatal("authenticated owner channel loss did not quiesce same issuer")
			}
		})
	}
}

func TestServiceDispatchWriteUncertaintyAndQuotaNeverResume(t *testing.T) {
	for _, kind := range []string{"write-failure", "cancel-before-completion", "quota", "inflight", "completion-replay"} {
		t.Run(kind, func(t *testing.T) {
			s, d, _, _, _ := dispatchTestOwner(t, 1)
			hello := dispatchTestFrame(t, d, dispatchHello)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			frame := []byte(dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.Status))
			reply, err := d.begin(ctx, frame)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "write-failure":
				if err := d.complete(reply, errors.New("private-write-error")); err == nil {
					t.Fatal("unknown write treated as success")
				}
			case "cancel-before-completion":
				cancel()
				if err := d.complete(reply, nil); err == nil {
					t.Fatal("canceled completion treated as success")
				}
			case "inflight":
				if _, err := d.begin(ctx, frame); err == nil {
					t.Fatal("second inflight request accepted")
				}
				if err := d.complete(reply, nil); err == nil {
					t.Fatal("late completion revived closed channel")
				}
			default:
				if err := d.complete(reply, nil); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "completion-replay" {
				if err := d.complete(reply, nil); err == nil {
					t.Fatal("completion reused")
				}
			}
			s.mu.Lock()
			issuer := s.issuer
			s.mu.Unlock()
			if !issuer.Snapshot().Closed {
				t.Fatal("quota/write uncertainty failed to quiesce same issuer")
			}
			fresh, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if _, err := d.begin(fresh, []byte(dispatchRequest(hello, 2, strings.Repeat("3", 32), servicecontrol.Status))); err == nil {
				t.Fatal("terminal channel resumed")
			}
		})
	}
}

func TestServiceDispatchCannotAdmitAtPublishedRunningBeforeNativeConfirmation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	plane := &serviceMemoryPlane{confirm: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	var legacy atomic.Int32
	source := lifetimePolicySource(func(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
		legacy.Add(1)
		return ephemeral.ApprovedPolicy{}, ErrProvisioningRejected
	})
	s, err := newService(ServiceConfig{Provisioning: source, Launcher: &lifetimeLauncher{}, Budgets: Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond}}, func(context.Context) (servicePlane, error) { return plane, nil })
	if err != nil {
		t.Fatal(err)
	}
	v := &dispatchTestVerifier{}
	d, err := newServiceDispatcher(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: 3}, v)
	if err != nil {
		t.Fatal(err)
	}
	v.owner = d
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), nil, make(chan ServiceState, 16)) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no confirm seam")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := d.begin(ctx, []byte(dispatchHello)); err == nil {
		t.Fatal("SCM status publication alone opened admission")
	}
	close(release)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("owner did not join")
	}
	if legacy.Load() != 0 {
		t.Fatal("early auth rejection read legacy approval")
	}
}
func TestServiceDispatchUnconfiguredCommandsCannotCreateApprovalOrBinding(t *testing.T) {
	s, d, _, _, legacy := dispatchTestOwner(t, 4)
	hello := dispatchTestFrame(t, d, dispatchHello)
	response := dispatchTestFrame(t, d, dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.PrepareIntent))
	if response.Result != "error" || response.Error == nil || response.Error.Code != "SERVICE_UNCONFIGURED" || response.Intent != nil || response.Approval != nil {
		t.Fatal("unconfigured candidate source invented approval")
	}
	frame := strings.Replace(dispatchRequest(hello, 2, strings.Repeat("3", 32), servicecontrol.ConfirmApproval), `"body":{}`, `"body":{"intentId":"44444444444444444444444444444444","previewDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`, 1)
	response = dispatchTestFrame(t, d, frame)
	if response.Error == nil || response.Error.Code != "SERVICE_UNCONFIGURED" {
		t.Fatal("unconfigured confirmation admitted authority")
	}
	if s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority || legacy.Load() != 0 {
		t.Fatal("denied commands reached legacy issuer")
	}
}
func TestServiceDispatchRegistrationAndProfileAreSingleOwnerAndFinite(t *testing.T) {
	for _, limit := range []uint64{0, 1025} {
		s := &Service{}
		if d, err := newServiceDispatcher(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: limit}, &dispatchTestVerifier{}); err == nil || d != nil {
			t.Fatal("unbounded profile accepted")
		}
	}
	s, _, v, _, _ := dispatchTestOwner(t, 4)
	if d, err := newServiceDispatcher(s, serviceDispatchProfile{ChannelNonce: strings.Repeat("b", 64), RequestLimit: 4}, v); err == nil || d != nil {
		t.Fatal("same Service accepted a replacement dispatcher")
	}
}

func TestServiceDispatchUnconfirmedRevocationReceiptPoisonsChannel(t *testing.T) {
	s, d, _, _, _ := dispatchTestOwner(t, 4)
	hello := dispatchTestFrame(t, d, dispatchHello)
	// 固定分類のmemory注入だけで、native impersonationやOS APIを実行しない。
	s.mu.Lock()
	s.stopCause = bootstrapauth.ErrFatal
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if reply, err := d.begin(ctx, []byte(dispatchRequest(hello, 1, strings.Repeat("2", 32), servicecontrol.RevokeAllAndQuiesce))); err == nil || reply != nil {
		t.Fatal("unconfirmed receipt became positive response")
	}
	if reply, err := d.begin(ctx, []byte(dispatchRequest(hello, 1, strings.Repeat("3", 32), servicecontrol.Status))); err == nil || reply != nil {
		t.Fatal("unconfirmed receipt allowed channel retry and unbounded read tickets")
	}
}
