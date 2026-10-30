package serviceprovision

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func TestRuntimeExecutionConstructorSelectsOnlyRuntimeMode(t *testing.T) {
	cfg := previewExecutionConfig()
	runtimeOwner, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil || !runtimeOwner.runtimeGuest || runtimeOwner.staging || runtimeOwner.preview == nil {
		t.Fatal("runtime mode was not selected independently")
	}
	staging, err := NewNativeStagingExecution(cfg)
	if err != nil || !staging.staging || staging.runtimeGuest {
		t.Fatal("staging mode changed")
	}
	preview, err := NewNativePreviewExecution(cfg)
	if err != nil || preview.staging || preview.runtimeGuest {
		t.Fatal("preview mode changed")
	}
	if owner, err := NewNativeRuntimeExecution(PreviewExecutionConfig{}); owner != nil || err == nil {
		t.Fatal("invalid runtime configuration accepted")
	}
	legacy, err := NewNativeExecution(ExecutionConfig{Anchor: rootAnchor(), CleanupRetry: time.Millisecond, Approval: &noApprovalAtConstruction{}})
	if err != nil || legacy.runtimeGuest || legacy.staging || legacy.preview != nil {
		t.Fatal("legacy mode changed")
	}
}

type runtimeQueueModel struct {
	native                                  nativeExecutionStage
	thread                                  uint64
	entered, release, closing, closeRelease chan struct{}
	returned, wrongThread                   bool
	partial                                 *childjob.Prepared
	closingOnce                             sync.Once
	cancelOnClose                           context.CancelFunc
	onPrepare                               func()
}

func (m *runtimeQueueModel) Initialize(ctx context.Context) (*servicehost.Service, error) {
	m.thread = executionTestThreadID()
	return m.native.Initialize(ctx)
}
func (m *runtimeQueueModel) Close(ctx context.Context) error {
	if m.thread != executionTestThreadID() {
		m.wrongThread = true
	}
	return m.native.Close(ctx)
}
func (m *runtimeQueueModel) PrepareRuntimeGuest(ctx context.Context, _ servicehost.RuntimeGuestTicket, _ servicehost.RuntimeGuestOwner, _ *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
	if m.thread != executionTestThreadID() {
		m.wrongThread = true
	}
	close(m.entered)
	<-m.release
	m.returned = true
	if m.onPrepare != nil {
		m.onPrepare()
	}
	return m.partial, childjob.GuestTokenExpectation{}, ctx.Err()
}
func (m *runtimeQueueModel) CloseRuntimeGuest(ctx context.Context, _ servicehost.RuntimeGuestTicket, _ servicehost.RuntimeGuestOwner, _ *serviceready.InspectionLease) error {
	if m.thread != executionTestThreadID() {
		m.wrongThread = true
	}
	if !m.returned {
		return ErrRootUnconfirmed
	}
	m.closingOnce.Do(func() { close(m.closing) })
	if m.cancelOnClose != nil {
		m.cancelOnClose()
		return nil // a late nil is not a fresh close acknowledgement
	}
	select {
	case <-m.closeRelease:
		return nil
	case <-ctx.Done():
		return ErrRootUnconfirmed
	}
}

func TestRuntimeRPCCancelRetainsPartialUntilOwnerCloseAcknowledges(t *testing.T) {
	p, root, _, _ := guestConfigurationFixture(t)
	e, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	m := &runtimeQueueModel{entered: make(chan struct{}), release: make(chan struct{}), closing: make(chan struct{}), closeRelease: make(chan struct{}), partial: &childjob.Prepared{}, native: nativeExecutionStage{cfg: e.cfg, preview: e.preview, staging: true, calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }, guest: func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) {
		return p.loadFixedGuest(ctx, root)
	}}}}
	ready := make(chan executionReady, 1)
	go e.own(rootContext(t), func() executionStage { return m }, ready)
	state := <-ready
	if state.err != nil {
		t.Fatal(state.err)
	}
	ctx, cancel := context.WithCancel(rootContext(t))
	prepareDone := make(chan error, 1)
	go func() {
		p, _, err := e.runtimeGuestRPC(ctx, servicehost.RuntimeGuestTicket{}, nil, false)
		if p != nil {
			prepareDone <- errors.New("canceled RPC returned material")
			return
		}
		prepareDone <- err
	}()
	select {
	case <-m.entered:
	case <-time.After(time.Second):
		t.Fatal("owner did not enter prepare")
	}
	cancel()
	if err := <-prepareDone; !errors.Is(err, ErrRootUnconfirmed) {
		t.Fatal("cancel was not unconfirmed")
	}
	closeDone := make(chan error, 1)
	go func() {
		_, _, err := e.runtimeGuestRPC(rootContext(t), servicehost.RuntimeGuestTicket{}, nil, true)
		closeDone <- err
	}()
	select {
	case <-m.closing:
		t.Fatal("close overtook SDK return")
	default:
	}
	close(m.release)
	select {
	case <-m.closing:
	case <-time.After(time.Second):
		t.Fatal("same owner did not begin close")
	}
	if e.runtimeAttempt == nil || e.runtimeAttempt.prepared != m.partial {
		t.Fatal("partial owner was lost")
	}
	select {
	case <-closeDone:
		t.Fatal("close acknowledged before actual return")
	default:
	}
	close(m.closeRelease)
	if err := <-closeDone; err != nil {
		t.Fatal("same owner cleanup failed")
	}
	if e.runtimeAttempt.prepared != nil || !e.runtimeAttempt.closed || m.wrongThread {
		t.Fatal("cleanup acknowledgement or thread identity lost")
	}
	if err := state.service.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.cleaned:
	case <-time.After(time.Second):
		t.Fatal("outer owner did not finish")
	}
}

func TestRuntimeOwnerReplyKeepsAdmissionAndRejectsDifferentLease(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	m := &runtimeQueueModel{thread: executionTestThreadID(), entered: make(chan struct{}), release: make(chan struct{}), closing: make(chan struct{}), closeRelease: make(chan struct{}), partial: &childjob.Prepared{}}
	close(m.release)
	close(m.closeRelease)
	e.stage = m
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ticket := servicehost.RuntimeGuestTicket{}
	result := e.runtimeOnOwner(ctx, ticket, nil, false)
	if result.err != nil || result.prepared != m.partial || ctx.Err() != nil || e.runtimeAttempt.prepared != m.partial {
		t.Fatal("reply lost admission or partial ownership")
	}
	wrong := e.runtimeOnOwner(ctx, ticket, &serviceready.InspectionLease{}, true)
	if wrong.err == nil || e.runtimeAttempt.prepared != m.partial {
		t.Fatal("different lease released the owner")
	}
	if result = e.runtimeOnOwner(ctx, ticket, nil, true); result.err != nil || e.runtimeAttempt.prepared != nil || !e.runtimeAttempt.closed || m.wrongThread {
		t.Fatal("matching owner did not acknowledge cleanup")
	}
	if result = e.runtimeOnOwner(ctx, ticket, nil, false); result.err == nil {
		t.Fatal("closed attempt was reused")
	}
}

func TestRuntimeOwnerLateCloseNilRetainsPartialUntilFreshAck(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	partial := &childjob.Prepared{}
	ticket := servicehost.RuntimeGuestTicket{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &runtimeQueueModel{thread: executionTestThreadID(), returned: true, closing: make(chan struct{}), closeRelease: make(chan struct{}), cancelOnClose: cancel}
	close(m.closeRelease)
	e.stage = m
	e.runtimeAttempt = &runtimeGuestAttempt{ticket: ticket, prepared: partial}
	result := e.runtimeOnOwner(ctx, ticket, nil, true)
	if !errors.Is(result.err, ErrRootUnconfirmed) || e.runtimeAttempt.prepared != partial || e.runtimeAttempt.closed {
		t.Fatal("late nil released the owned partial")
	}
	m.cancelOnClose = nil
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	result = e.runtimeOnOwner(fresh, ticket, nil, true)
	if result.err != nil || e.runtimeAttempt.prepared != nil || !e.runtimeAttempt.closed {
		t.Fatal("fresh close acknowledgement did not release partial")
	}
}

func TestRuntimeOwnerRejectsNilMaterialWithoutError(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	m := &runtimeQueueModel{thread: executionTestThreadID(), entered: make(chan struct{}), release: make(chan struct{}), closing: make(chan struct{}), closeRelease: make(chan struct{})}
	close(m.release)
	e.stage = m
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := e.runtimeOnOwner(ctx, servicehost.RuntimeGuestTicket{}, nil, false)
	if !errors.Is(result.err, ErrRootUnconfirmed) || result.prepared != nil || e.runtimeAttempt == nil {
		t.Fatal("nil material was acknowledged or attempt identity lost")
	}
}

type runtimeDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c *runtimeDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestRuntimeOwnerRejectsDeadlineEvenBeforeContextNotification(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	m := &runtimeQueueModel{thread: executionTestThreadID(), entered: make(chan struct{}), release: make(chan struct{}), partial: &childjob.Prepared{}}
	close(m.release)
	e.stage = m
	ctx := &runtimeDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	result := e.runtimeOnOwner(ctx, servicehost.RuntimeGuestTicket{}, nil, false)
	if result.err == nil || result.prepared != nil || e.runtimeAttempt != nil {
		t.Fatal("expired admission reached runtime stage")
	}
	select {
	case <-m.entered:
		t.Fatal("expired admission acquired work")
	default:
	}
}

func TestRuntimeOwnerRetainsPartialIfDeadlineExpiresDuringPrepare(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	e, err := NewNativeRuntimeExecution(runtimeExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := &runtimeDeadlineContext{Context: context.Background(), deadline: time.Now().Add(time.Second)}
	partial := &childjob.Prepared{}
	m := &runtimeQueueModel{thread: executionTestThreadID(), entered: make(chan struct{}), release: make(chan struct{}), partial: partial, onPrepare: func() { ctx.deadline = time.Now().Add(-time.Second) }}
	close(m.release)
	e.stage = m
	result := e.runtimeOnOwner(ctx, servicehost.RuntimeGuestTicket{}, nil, false)
	if !errors.Is(result.err, ErrRootUnconfirmed) || e.runtimeAttempt == nil || e.runtimeAttempt.prepared != partial {
		t.Fatal("late partial was lost or deadline treated as success")
	}
}
