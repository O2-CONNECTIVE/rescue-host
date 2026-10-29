package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type acceptedTestResult struct {
	conn net.Conn
	err  error
}
type acceptedMemorySource struct {
	entered chan struct{}
	results chan acceptedTestResult
	stopped chan struct{}
	once    sync.Once
}

func newAcceptedMemorySource() *acceptedMemorySource {
	return &acceptedMemorySource{entered: make(chan struct{}, 8), results: make(chan acceptedTestResult, 8), stopped: make(chan struct{})}
}
func (p *acceptedMemorySource) Accept(ctx context.Context) (net.Conn, error) {
	p.entered <- struct{}{}
	select {
	case r := <-p.results:
		return r.conn, r.err
	case <-p.stopped:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (p *acceptedMemorySource) Close(context.Context) error {
	p.once.Do(func() { close(p.stopped) })
	return nil
}
func acceptedTestSession(t *testing.T) (*Session, Config) {
	t.Helper()
	c, _ := config(t, "full")
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, s) })
	return s, c
}

func acceptedActivate(s *Session, id Identity) error {
	deadline := time.Now().Add(time.Second)
	if s.accepted != nil {
		deadline, _ = s.accepted.ctx.Deadline()
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return s.ActivateForStart(ctx, id, deadline)
}

func TestAcceptedTransportRejectsUninitializedSession(t *testing.T) {
	source := newAcceptedMemorySource()
	if err := (&Session{}).AttachAcceptedSource(source, time.Now().Add(time.Second), time.Second); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("uninitialized session: got %v, want ErrInvalidConfig", err)
	}
}

func TestAcceptedTransportRejectsDirectActivation(t *testing.T) {
	s, c := acceptedTestSession(t)
	source := newAcceptedMemorySource()
	if err := s.AttachAcceptedSource(source, time.Now().Add(time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := s.Activate(c.Identity); err == nil {
		t.Fatal("attached source bypassed coordinated start")
	}
	select {
	case <-source.entered:
		t.Fatal("source accepted before coordinated start")
	default:
	}
}

type cancelDuringStart struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelDuringStart) Err() error {
	err := c.Context.Err()
	if err == nil {
		c.once.Do(c.cancel)
	}
	return err
}

func TestAcceptedTransportCanceledAtStartDoesNotAccept(t *testing.T) {
	s, config := acceptedTestSession(t)
	source := newAcceptedMemorySource()
	deadline := time.Now().Add(time.Second)
	if err := s.AttachAcceptedSource(source, deadline, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(config.Identity); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	ctx := &cancelDuringStart{Context: base, cancel: cancel}
	if err := s.ActivateForStart(ctx, config.Identity, deadline); err == nil {
		t.Fatal("canceled start succeeded")
	}
	if state := s.Snapshot().State; state == "active" {
		t.Fatal("canceled start left authority active")
	}
	select {
	case <-source.entered:
		t.Fatal("canceled start accepted a connection")
	default:
	}
}

func TestAcceptedTransportRejectsStartDeadlineBeyondSource(t *testing.T) {
	s, config := acceptedTestSession(t)
	source := newAcceptedMemorySource()
	sourceDeadline := time.Now().Add(time.Second)
	if err := s.AttachAcceptedSource(source, sourceDeadline, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(config.Identity); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), sourceDeadline.Add(time.Second))
	defer cancel()
	if err := s.ActivateForStart(ctx, config.Identity, sourceDeadline.Add(time.Second)); err == nil {
		t.Fatal("start exceeded the source deadline")
	}
	select {
	case <-source.entered:
		t.Fatal("source accepted beyond its approved deadline")
	default:
	}
}

type delayedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestAcceptedTransportRejectsElapsedRequestDeadlineBeforeTimerSignal(t *testing.T) {
	s, config := acceptedTestSession(t)
	source := newAcceptedMemorySource()
	deadline := time.Now().Add(time.Second)
	if err := s.AttachAcceptedSource(source, deadline, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(config.Identity); err != nil {
		t.Fatal(err)
	}
	ctx := delayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	if err := s.ActivateForStart(ctx, config.Identity, deadline); err == nil {
		t.Fatal("elapsed request deadline was accepted")
	}
	select {
	case <-source.entered:
		t.Fatal("source accepted after request deadline")
	default:
	}
}

func TestAcceptedTransportWaitsForActualSessionActivation(t *testing.T) {
	s, c := acceptedTestSession(t)
	source := newAcceptedMemorySource()
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.entered:
		t.Fatal("source accepted before actual Session activation")
	case <-time.After(20 * time.Millisecond):
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("active Session did not start accepting")
	}
}

type acceptedMemoryAddr struct{}

func (acceptedMemoryAddr) Network() string { return "memory" }
func (acceptedMemoryAddr) String() string  { return "memory" }

type acceptedMemoryConn struct {
	closes        atomic.Int32
	reads         atomic.Int32
	writes        atomic.Int32
	deadlineError error
	closeGate     <-chan struct{}
	closeError    atomic.Bool
	closed        chan struct{}
	closeOnce     sync.Once
}

func newAcceptedMemoryConn() *acceptedMemoryConn {
	return &acceptedMemoryConn{deadlineError: errors.New("memory pre-handshake deadline refusal"), closed: make(chan struct{})}
}
func (c *acceptedMemoryConn) Read([]byte) (int, error)  { c.reads.Add(1); return 0, io.EOF }
func (c *acceptedMemoryConn) Write([]byte) (int, error) { c.writes.Add(1); return 0, io.ErrClosedPipe }
func (c *acceptedMemoryConn) Close() error {
	c.closes.Add(1)
	if c.closeGate != nil {
		<-c.closeGate
	}
	if c.closeError.Load() {
		return errors.New("memory close refusal")
	}
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (*acceptedMemoryConn) LocalAddr() net.Addr              { return acceptedMemoryAddr{} }
func (*acceptedMemoryConn) RemoteAddr() net.Addr             { return acceptedMemoryAddr{} }
func (c *acceptedMemoryConn) SetDeadline(time.Time) error    { return c.deadlineError }
func (*acceptedMemoryConn) SetReadDeadline(time.Time) error  { return nil }
func (*acceptedMemoryConn) SetWriteDeadline(time.Time) error { return nil }
func TestAcceptedTransportPostTransferSentinelCannotImplyCallerOwnership(t *testing.T) {
	s, c := acceptedTestSession(t)
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	conn := newAcceptedMemoryConn()
	conn.deadlineError = fmt.Errorf("memory wrapped error: %w", ErrNotAccepted)
	if err := s.Serve(conn); errors.Is(err, ErrNotAccepted) {
		t.Fatal("post-transfer error falsely returned caller ownership")
	}
	closeSession(t, s)
	select {
	case <-conn.closed:
	default:
		t.Fatal("same Session did not retain/close transferred connection")
	}
	if conn.reads.Load() != 0 || conn.writes.Load() != 0 {
		t.Fatal("test crossed pre-handshake boundary")
	}
}

func TestAcceptedTransportDeliversRepeatedConnectionsToSameSessionBeforeHandshake(t *testing.T) {
	s, c := acceptedTestSession(t)
	source := newAcceptedMemorySource()
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(2*time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		conn := newAcceptedMemoryConn()
		select {
		case <-source.entered:
		case <-time.After(time.Second):
			t.Fatal("next bounded Accept did not start")
		}
		source.results <- acceptedTestResult{conn: conn}
		select {
		case <-conn.closed:
		case <-time.After(time.Second):
			t.Fatal("accepted connection was dropped instead of same Session ownership")
		}
		if conn.reads.Load() != 0 || conn.writes.Load() != 0 {
			t.Fatal("test crossed pre-handshake boundary")
		}
	}
	if s.Snapshot().Identity != c.Identity {
		t.Fatal("transport source replaced Session identity")
	}
	closeSession(t, s)
}

func TestAcceptedTransportCapacityIncludesTransferredClosePending(t *testing.T) {
	c, _ := config(t, "full")
	c.MaxTransports = 1
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, s) })
	source := newAcceptedMemorySource()
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(2*time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	conn := newAcceptedMemoryConn()
	conn.closeGate = gate
	<-source.entered
	source.results <- acceptedTestResult{conn: conn}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for conn.closes.Load() == 0 {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("close did not start")
		}
	}
	select {
	case <-source.entered:
		t.Fatal("new Accept exceeded capacity while same Session still owned old connection")
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(gate) })
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("confirmed transport close did not release admission capacity")
	}
	if conn.reads.Load() != 0 || conn.writes.Load() != 0 {
		t.Fatal("test crossed pre-handshake boundary")
	}
}

type acceptedLateSource struct {
	entered chan struct{}
	closed  chan struct{}
	release <-chan struct{}
	conn    net.Conn
	once    sync.Once
}

func (p *acceptedLateSource) Accept(context.Context) (net.Conn, error) {
	close(p.entered)
	<-p.release
	return p.conn, io.ErrUnexpectedEOF
}
func (p *acceptedLateSource) Close(context.Context) error {
	p.once.Do(func() { close(p.closed) })
	return nil
}
func TestAcceptedTransportLateAcceptCompletionKeepsPartialConnectionOwned(t *testing.T) {
	s, c := acceptedTestSession(t)
	gate := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	conn := newAcceptedMemoryConn()
	source := &acceptedLateSource{entered: make(chan struct{}), closed: make(chan struct{}), release: gate, conn: conn}
	if err := s.AttachAcceptedSource(source, time.Now().Add(2*time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("Accept did not start")
	}
	first, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := s.Close(first)
	stop()
	if err == nil || s.Snapshot().State == "terminated" {
		t.Fatal("source Close request substituted for actual Accept completion")
	}
	release.Do(func() { close(gate) })
	closeSession(t, s)
	select {
	case <-conn.closed:
	default:
		t.Fatal("late partial connection was lost")
	}
	if conn.reads.Load() != 0 || conn.writes.Load() != 0 {
		t.Fatal("late rejected connection reached SSH")
	}
}

type acceptedErrorSource struct {
	entered    chan struct{}
	closeError atomic.Bool
	once       sync.Once
	conn       net.Conn
}

func (p *acceptedErrorSource) Accept(ctx context.Context) (net.Conn, error) {
	p.once.Do(func() { close(p.entered) })
	if p.conn != nil {
		return p.conn, io.ErrUnexpectedEOF
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *acceptedErrorSource) Close(context.Context) error {
	if p.closeError.Load() {
		return errors.New("memory source close refusal")
	}
	return nil
}
func TestAcceptedTransportFailedRejectedCloseRetainsUntilSameOwnerRetry(t *testing.T) {
	s, c := acceptedTestSession(t)
	conn := newAcceptedMemoryConn()
	conn.closeError.Store(true)
	source := &acceptedErrorSource{entered: make(chan struct{}), conn: conn}
	t.Cleanup(func() { conn.closeError.Store(false) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(2*time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("source did not run")
	}
	first, stop := context.WithTimeout(context.Background(), time.Second)
	err := s.Close(first)
	stop()
	if err == nil || s.Snapshot().State == "terminated" {
		t.Fatal("failed rejected-connection Close was forgotten")
	}
	conn.closeError.Store(false)
	closeSession(t, s)
	select {
	case <-conn.closed:
	default:
		t.Fatal("same owner lost failed-close connection")
	}
	if conn.reads.Load() != 0 || conn.writes.Load() != 0 {
		t.Fatal("partial rejected connection reached SSH")
	}
}
func TestAcceptedTransportFailedSourceCloseNeverTerminatesRawSession(t *testing.T) {
	s, c := acceptedTestSession(t)
	source := &acceptedErrorSource{entered: make(chan struct{})}
	source.closeError.Store(true)
	t.Cleanup(func() { source.closeError.Store(false) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(2*time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	<-source.entered
	first, stop := context.WithTimeout(context.Background(), time.Second)
	err := s.Close(first)
	stop()
	if err == nil || s.Snapshot().State == "terminated" {
		t.Fatal("raw Session terminated despite unconfirmed source Close")
	}
	source.closeError.Store(false)
	closeSession(t, s)
}
func TestAcceptedTransportDuplicateOrLateAttachLeavesCallerOwnership(t *testing.T) {
	for _, stage := range []string{"duplicate", "ready", "active"} {
		t.Run(stage, func(t *testing.T) {
			s, c := acceptedTestSession(t)
			first := newAcceptedMemorySource()
			second := newAcceptedMemorySource()
			t.Cleanup(func() { _ = first.Close(context.Background()); _ = second.Close(context.Background()) })
			if stage == "duplicate" {
				if err := s.AttachAcceptedSource(first, time.Now().Add(time.Second), time.Second); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "ready" || stage == "active" {
				if err := s.Ready(c.Identity); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "active" {
				if err := acceptedActivate(s, c.Identity); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.AttachAcceptedSource(second, time.Now().Add(time.Second), time.Second); err == nil {
				t.Fatal("second/late source attached")
			}
			select {
			case <-second.entered:
				t.Fatal("rejected source was consumed")
			case <-second.stopped:
				t.Fatal("rejected source was closed by Session")
			default:
			}
		})
	}
}

type acceptedCloseOnlySource struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (p *acceptedCloseOnlySource) Accept(context.Context) (net.Conn, error) {
	close(p.entered)
	<-p.closed
	return nil, context.Canceled
}
func (p *acceptedCloseOnlySource) Close(context.Context) error {
	p.once.Do(func() { close(p.closed) })
	return nil
}
func TestAcceptedTransportHardDeadlineClosesSourceIndependentlyOfPendingAccept(t *testing.T) {
	s, c := acceptedTestSession(t)
	source := &acceptedCloseOnlySource{entered: make(chan struct{}), closed: make(chan struct{})}
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(100*time.Millisecond), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("memory Accept did not start")
	}
	select {
	case <-source.closed:
	case <-time.After(400 * time.Millisecond):
		t.Fatal("hard deadline waited for blocked Accept instead of independent source Close")
	}
	if s.Snapshot().State == "active" {
		t.Fatal("ingress deadline did not revoke the same Session")
	}
	closeSession(t, s)
}

func TestAcceptedTransportCanceledCloseNeverReportsPositiveConfirmation(t *testing.T) {
	s, _ := acceptedTestSession(t)
	closeSession(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for n := 0; n < 32; n++ {
		if err := s.Close(ctx); err == nil {
			t.Fatal("canceled caller budget produced positive Session Close")
		}
	}
}

type acceptedLateNilSource struct {
	started chan context.Context
	release <-chan struct{}
	calls   atomic.Int32
	fresh   atomic.Bool
}

func (p *acceptedLateNilSource) Accept(ctx context.Context) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *acceptedLateNilSource) Close(ctx context.Context) error {
	if p.calls.Add(1) == 1 {
		p.started <- ctx
		<-p.release
		return nil
	}
	if !p.fresh.Load() {
		return errors.New("memory fresh confirmation not yet allowed")
	}
	return nil
}
func TestAcceptedTransportLateNilAfterOwnCleanupBudgetNeedsFreshConfirmation(t *testing.T) {
	s, c := acceptedTestSession(t)
	gate := make(chan struct{})
	var release sync.Once
	source := &acceptedLateNilSource{started: make(chan context.Context, 1), release: gate}
	t.Cleanup(func() { source.fresh.Store(true); release.Do(func() { close(gate) }) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(2*time.Second), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	outer, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	done := make(chan error, 1)
	go func() { done <- s.Close(outer) }()
	var own context.Context
	select {
	case own = <-source.started:
	case <-outer.Done():
		t.Fatal("source Close did not start")
	}
	<-own.Done()
	if outer.Err() != nil {
		t.Fatal("test did not preserve independent outer budget")
	}
	release.Do(func() { close(gate) })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("late nil after own budget became positive cleanup")
		}
	case <-outer.Done():
		t.Fatal("source owner did not return unconfirmed")
	}
	if s.Snapshot().State == "terminated" {
		t.Fatal("late nil terminated raw Session")
	}
	source.fresh.Store(true)
	closeSession(t, s)
}

func TestAcceptedTransportReservedCapacityAlsoBoundsDirectServe(t *testing.T) {
	c, _ := config(t, "full")
	c.MaxTransports = 1
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSession(t, s) })
	source := newAcceptedMemorySource()
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	if err := s.AttachAcceptedSource(source, time.Now().Add(time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(c.Identity); err != nil {
		t.Fatal(err)
	}
	if err := acceptedActivate(s, c.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("no reserved Accept")
	}
	conn := newAcceptedMemoryConn()
	if err := s.Serve(conn); err != ErrNotAccepted {
		t.Fatal("direct Serve ignored capacity already reserved for ingress Accept")
	}
	if conn.closes.Load() != 0 {
		t.Fatal("nonaccepted direct Serve stole caller connection")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}
