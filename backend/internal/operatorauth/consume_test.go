package operatorauth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
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
