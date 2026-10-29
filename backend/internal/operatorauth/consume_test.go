package operatorauth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollectorConsumesOnlyItsOwnProofOnce(t *testing.T) {
	c := (&collectorFixture{eof: true}).collector()
	r, err := c.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	other := (&collectorFixture{eof: true}).collector()
	if _, err = other.Consume(requestContext(t), r); err == nil {
		t.Fatal("foreign proof accepted")
	}
	ctx := requestContext(t)
	var successes atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			b, e := c.Consume(ctx, r)
			if e == nil {
				if string(b) != string(requestFrame()) {
					t.Error("frame changed")
				}
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatal("proof consumption count", successes.Load())
	}
}

func TestCollectorDirectDrainReceivesStopAndOriginalCancellation(t *testing.T) {
	for _, mode := range []string{"close", "original"} {
		t.Run(mode, func(t *testing.T) {
			original, stopOriginal := context.WithCancel(requestContext(t))
			defer stopOriginal()
			caller, stopCaller := context.WithCancel(requestContext(t))
			defer stopCaller()
			entered := make(chan struct{})
			noop := func() error { return nil }
			withContext := func(context.Context) error { return nil }
			c := &Collector{request: original, cleanup: time.Second, ops: collectorOps{check: withContext, drain: func(ctx context.Context) (reportResult, error) {
				close(entered)
				<-ctx.Done()
				return reportResult{}, ctx.Err()
			}, terminate: noop, wait: func(context.Context) (uint32, error) { return 0, nil }, closeIO: withContext, closeProcess: noop, revoke: noop, reclaim: withContext}}
			done := make(chan error, 1)
			go func() { _, err := c.Collect(caller); done <- err }()
			<-entered
			if mode == "close" {
				_ = c.Close(requestContext(t))
			} else {
				stopOriginal()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled drain succeeded")
				}
			case <-time.After(100 * time.Millisecond):
				stopCaller()
				<-done
				t.Fatal("stop did not reach direct collector drain")
			}
		})
	}
}

func TestPreparedBatchConcreteRevokeCancelsDirectCollectorDrain(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	b, err := startBatch(requestContext(t), s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	collector := b.Collector()
	entered := make(chan struct{})
	b.collector.ops.drain = func(ctx context.Context) (reportResult, error) {
		close(entered)
		<-ctx.Done()
		return reportResult{}, ctx.Err()
	}
	caller, cancel := context.WithCancel(requestContext(t))
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := b.Collector().Collect(caller); done <- e }()
	<-entered
	if err = b.Close(requestContext(t)); err != nil && (!errors.Is(err, ErrUnconfirmed) || collectorCleanupFatal(err)) {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("cancelled drain succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("concrete owner revocation lost direct cancellation")
	}
	if b.Collector() != collector || b.owner != owner.owner {
		t.Fatal("canceled drain replaced retained owners")
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal("joined collector cleanup remained unconfirmed", err)
	}
}

func TestCollectorFailedConsumptionBurnsProof(t *testing.T) {
	c := (&collectorFixture{eof: true}).collector()
	r, err := c.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(requestContext(t))
	cancel()
	if _, err = c.Consume(ctx, r); err == nil {
		t.Fatal("cancelled proof accepted")
	}
	if _, err = c.Consume(requestContext(t), r); err == nil {
		t.Fatal("failed proof revived")
	}
	if _, err = c.Consume(requestContext(t), &Read{}); err == nil {
		t.Fatal("fabricated proof accepted")
	}
}

func TestCollectorKeepsBoundOriginalRequestLifetime(t *testing.T) {
	c := (&collectorFixture{eof: true}).collector()
	original, cancel := context.WithCancel(requestContext(t))
	defer cancel()
	c.request = original
	r, err := c.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = c.Consume(requestContext(t), r); err == nil {
		t.Fatal("original request cancellation was extended")
	}
}

func TestCollectorOriginalCancellationDuringCompletionRejectsProof(t *testing.T) {
	c := (&collectorFixture{eof: true}).collector()
	original, cancel := context.WithCancel(requestContext(t))
	defer cancel()
	c.request = original
	c.ops.closeProcess = func() error { cancel(); return nil }
	if proof, err := c.Collect(requestContext(t)); proof != nil || err == nil {
		t.Fatal("completion published cancelled proof")
	}
}
