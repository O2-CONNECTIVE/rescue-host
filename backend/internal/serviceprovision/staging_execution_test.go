package serviceprovision

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"sync"
	"testing"
)

func TestNativeStagingPreviewHoldsGuestBundleBeforeAnyStrongAcquisition(t *testing.T) {
	p, root, _, _ := guestConfigurationFixture(t)
	limits := previewExecutionConfig().Limits
	e, err := NewNativeStagingExecution(previewExecutionConfig())
	if err != nil || e == nil || !e.staging {
		t.Fatal("explicit staging entry absent", err)
	}
	strongCalls := 0
	stage := &nativeExecutionStage{cfg: e.cfg, preview: &limits, staging: true, calls: nativeExecutionCalls{
		prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil },
		guest: func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) {
			return p.loadFixedGuest(ctx, root)
		},
		launch: func(context.Context, *Prepared) (*LaunchToken, error) { strongCalls++; return nil, ErrLaunchDenied },
	}}
	service, err := stage.Initialize(rootContext(t))
	if err != nil || service == nil || stage.guest == nil || stage.previewSnapshot == nil || strongCalls != 0 {
		t.Fatal("staging preview did not retain guest first", err)
	}
	digest, err := stage.guest.Digest(rootContext(t))
	if err != nil || stage.previewSnapshot.ConfigDigest != digest {
		t.Fatal("guest layout outside confirmation digest", err)
	}
	if err = stage.VerifyPreviewConfirmation(rootContext(t), *stage.previewSnapshot); err != nil {
		t.Fatal(err)
	}
	if err = service.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if err = stage.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
}

// This is an owner-queue/SDK memory model, not a native Endpoint authentication
// proof. The real constructor and outer-finalizer RPC own the service lifetime.
type stagingQueueModel struct {
	native                   nativeExecutionStage
	entered, release, closed chan struct{}
	thread                   uint64
	returned, wrongThread    bool
}

func (m *stagingQueueModel) Initialize(ctx context.Context) (*servicehost.Service, error) {
	m.thread = executionTestThreadID()
	return m.native.Initialize(ctx)
}
func (m *stagingQueueModel) Close(ctx context.Context) error {
	if m.thread != executionTestThreadID() {
		m.wrongThread = true
	}
	return m.native.Close(ctx)
}
func (m *stagingQueueModel) PrepareGuest(ctx context.Context, _ servicehost.GuestStagingTicket, _ servicehost.GuestStagingOwner, _ *serviceready.InspectionLease) error {
	if m.thread != executionTestThreadID() {
		m.wrongThread = true
	}
	close(m.entered)
	<-m.release
	m.returned = true
	return ctx.Err()
}
func (m *stagingQueueModel) CloseGuest(context.Context, servicehost.GuestStagingTicket, servicehost.GuestStagingOwner, *serviceready.InspectionLease) error {
	if m.thread != executionTestThreadID() {
		m.wrongThread = true
	}
	if !m.returned {
		return ErrRootUnconfirmed
	}
	close(m.closed)
	return nil
}
func TestStagingRPCCancellationRetainsOriginalOwnerUntilSDKReturnAndQueuedClose(t *testing.T) {
	p, root, _, _ := guestConfigurationFixture(t)
	e, err := NewNativeStagingExecution(previewExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	m := &stagingQueueModel{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), native: nativeExecutionStage{cfg: e.cfg, preview: e.preview, staging: true, calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }, guest: func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) {
		return p.loadFixedGuest(ctx, root)
	}}}}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(m.release) }) }
	defer release()
	bounded := rootContext(t)
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-bounded.Done():
			t.Fatal("owner model did not reach finite barrier")
		}
	}
	ready := make(chan executionReady, 1)
	go e.own(rootContext(t), func() executionStage { return m }, ready)
	var state executionReady
	select {
	case state = <-ready:
	case <-bounded.Done():
		t.Fatal("owner did not initialize")
	}
	if state.err != nil {
		t.Fatal(state.err)
	}
	ctx, cancel := context.WithCancel(rootContext(t))
	acquired := make(chan error, 1)
	go func() { acquired <- e.guestRPC(ctx, servicehost.GuestStagingTicket{}, nil, false) }()
	wait(m.entered)
	cancel()
	if err = <-acquired; !errors.Is(err, ErrRootUnconfirmed) {
		t.Fatal("RPC cancel did not return bounded unknown", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- e.guestRPC(rootContext(t), servicehost.GuestStagingTicket{}, nil, true) }()
	select {
	case <-m.closed:
		t.Fatal("cleanup overtook in-flight original SDK")
	default:
	}
	if m.native.guest.closed.Load() || m.native.prepared == nil {
		t.Fatal("outer refs released on caller cancel")
	}
	release()
	if err = <-closeDone; err != nil {
		t.Fatal(err)
	}
	wait(m.closed)
	if err = state.service.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	wait(e.cleaned)
	if m.wrongThread || !m.returned || m.native.prepared != nil || m.native.guest != nil {
		t.Fatal("owner/outer cleanup not joined")
	}
}
