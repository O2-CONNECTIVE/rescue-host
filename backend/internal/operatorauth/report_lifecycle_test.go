package operatorauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestReportCloseWithoutDeadlineBurnsReader(t *testing.T) {
	k := &reportFake{replies: []reportReply{{data: "frame\n", n: 6}, {err: errReportBrokenPipe}}}
	p, err := newReportPair(reportContext(t), reportSetup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.releaseWriter(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	if err = p.close(context.Background()); err == nil {
		t.Fatal("unbounded cleanup accepted")
	}
	if got, err := p.drain(reportContext(t)); err == nil || got.eof {
		t.Fatal("close resurrected reader")
	}
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
}

type reportPendingOp struct {
	mu                        sync.Mutex
	done, cancelled, released bool
}

func (o *reportPendingOp) poll() (bool, uint32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.done, 0, context.Canceled
}
func (o *reportPendingOp) cancel() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cancelled = true
	return nil
}
func (o *reportPendingOp) release() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.released = true
	return nil
}

type reportPendingKernel struct {
	reportFake
	op     *reportPendingOp
	cancel context.CancelFunc
}

func (k *reportPendingKernel) start(kind reportKind, h uintptr, b []byte) (reportOperation, error) {
	if kind == reportConnect {
		return k.reportFake.start(kind, h, b)
	}
	if k.cancel != nil {
		k.cancel()
	}
	return k.op, nil
}
func TestReportCancellationRetainsOwnerUntilCompletion(t *testing.T) {
	k := &reportPendingKernel{op: &reportPendingOp{}}
	p, err := newReportPair(reportContext(t), reportSetup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.releaseWriter(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k.cancel = cancel
	if got, err := p.drain(ctx); !errors.Is(err, ErrUnconfirmed) || got.eof {
		t.Fatalf("report=%+v error=%v", got, err)
	}
	if err = p.close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("uncompleted read reclaimed", err)
	}
	if len(k.closed) != 2 || !k.op.cancelled || k.op.released {
		t.Fatal("live request resources released")
	}
	k.op.mu.Lock()
	k.op.done = true
	k.op.mu.Unlock()
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	if len(k.closed) != 3 || k.closed[2] != 11 || !k.op.released {
		t.Fatal("completion did not release retained reader")
	}
}

type reportSelfCloseKernel struct {
	reportFake
	pair *reportPair
}

func (k *reportSelfCloseKernel) start(kind reportKind, h uintptr, b []byte) (reportOperation, error) {
	if kind == reportRead {
		_ = k.pair.close(reportContextForClose())
	}
	return k.reportFake.start(kind, h, b)
}
func reportContextForClose() context.Context {
	// Already cancelled but bounded: one cancel/poll, with no blocking cleanup.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	cancel()
	return ctx
}
func TestReportSelfCloseCannotManufactureEOF(t *testing.T) {
	k := &reportSelfCloseKernel{reportFake: reportFake{replies: []reportReply{{data: "frame\n", n: 6}, {err: errReportBrokenPipe}}}}
	p, err := newReportPair(reportContext(t), reportSetup(), k)
	if err != nil {
		t.Fatal(err)
	}
	k.pair = p
	if err = p.releaseWriter(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	if got, err := p.drain(reportContext(t)); err == nil || got.eof {
		t.Fatal("self close accepted as report EOF")
	}
	if len(k.replies) != 1 {
		t.Fatal("self closed reader started a second read")
	}
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestReportExpiredCleanupRetainsUnclosedHandles(t *testing.T) {
	k := &reportFake{}
	p, err := newReportPair(reportContext(t), reportSetup(), k)
	if err != nil {
		t.Fatal(err)
	}
	before := len(k.closed)
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err = p.close(expired); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("expired cleanup reported complete: %v", err)
	}
	if len(k.closed) != before {
		t.Fatal("expired cleanup released unconfirmed handles")
	}
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	if len(k.closed) != before+3 {
		t.Fatal("same owner failed to recover retained handles")
	}
}

type panicOpenReport struct{ reportFake }

func (*panicOpenReport) open(string) (uintptr, error) { panic("synthetic open fault") }
func TestReportAcquisitionPanicReturnsPartialOwner(t *testing.T) {
	k := &panicOpenReport{}
	var p *reportPair
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = ErrUnconfirmed
			}
		}()
		p, err = newReportPair(reportContext(t), reportSetup(), k)
	}()
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("partial owner lost: pair=%v err=%v", p, err)
	}
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	if len(k.closed) != 1 || k.closed[0] != 11 {
		t.Fatal("partial reader not reclaimed", k.closed)
	}
}

type blockingReportOp struct {
	once             sync.Once
	entered, release chan struct{}
}

func (o *blockingReportOp) poll() (bool, uint32, error) {
	o.once.Do(func() { close(o.entered); <-o.release })
	return true, 0, errReportBrokenPipe
}
func (*blockingReportOp) cancel() error    { return nil }
func (*blockingReportOp) releaseOp() error { return nil }

type blockingReportOperation struct{ op *blockingReportOp }

func (o blockingReportOperation) poll() (bool, uint32, error) { return o.op.poll() }
func (o blockingReportOperation) cancel() error               { return o.op.cancel() }
func (o blockingReportOperation) release() error              { return o.op.releaseOp() }

type blockingReportKernel struct {
	reportFake
	op *blockingReportOp
}

func (k *blockingReportKernel) start(kind reportKind, h uintptr, b []byte) (reportOperation, error) {
	if kind == reportRead {
		return blockingReportOperation{k.op}, nil
	}
	return k.reportFake.start(kind, h, b)
}
func TestReportCloseDoesNotWaitPastDeadlineBehindPoll(t *testing.T) {
	k := &blockingReportKernel{op: &blockingReportOp{entered: make(chan struct{}), release: make(chan struct{})}}
	p, err := newReportPair(reportContext(t), reportSetup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.releaseWriter(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	readCtx := reportContext(t)
	drained := make(chan struct{})
	go func() { _, _ = p.drain(readCtx); close(drained) }()
	<-k.op.entered
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.close(ctx) }()
	blocked := false
	select {
	case err = <-done:
	case <-time.After(100 * time.Millisecond):
		blocked = true
	}
	close(k.op.release)
	if blocked {
		err = <-done
	}
	<-drained
	if blocked || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("bounded Close blocked=%v error=%v", blocked, err)
	}
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
}
