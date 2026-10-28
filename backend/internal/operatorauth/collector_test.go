package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type collectorFixture struct {
	events                               []string
	eof                                  bool
	exit                                 uint32
	drainErr, waitErr, ioErr, reclaimErr error
	onWait                               func()
}

func (f *collectorFixture) collector() *Collector {
	c := &Collector{cleanup: time.Second}
	c.ops = collectorOps{
		check: func(context.Context) error { f.events = append(f.events, "binding"); return nil },
		drain: func(context.Context) (reportResult, error) {
			f.events = append(f.events, "drain-eof")
			return reportResult{requestFrame(), f.eof}, f.drainErr
		},
		terminate: func() error { f.events = append(f.events, "terminate"); return nil },
		wait: func(context.Context) (uint32, error) {
			f.events = append(f.events, "wait")
			if f.onWait != nil {
				f.onWait()
			}
			return f.exit, f.waitErr
		},
		closeIO:      func(context.Context) error { f.events = append(f.events, "io-close-join"); return f.ioErr },
		closeProcess: func() error { f.events = append(f.events, "process-close"); return nil },
		revoke:       func() error { f.events = append(f.events, "revoke"); return nil },
		reclaim:      func(context.Context) error { f.events = append(f.events, "workload-reclaim"); return f.reclaimErr },
	}
	return c
}

func TestCollectorDrainsBeforeWaitAndProducesOneUseRead(t *testing.T) {
	f := &collectorFixture{eof: true}
	c := f.collector()
	r, err := c.Collect(requestContext(t))
	if err != nil || r == nil {
		t.Fatal(err)
	}
	want := []string{"binding", "drain-eof", "wait", "binding", "io-close-join", "process-close"}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatal(f.events)
	}
	b, err := r.Consume(requestContext(t))
	if err != nil || string(b) != string(requestFrame()) {
		t.Fatal(err, string(b))
	}
	b[0] = '!'
	if _, err = r.Consume(requestContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("reused read", err)
	}
}

func TestCancelledCloseRevokesBeforeCheckingCleanupBudget(t *testing.T) {
	f := &collectorFixture{eof: true}
	c := f.collector()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(c.Close(ctx), ErrUnconfirmed) {
		t.Fatal("cancelled cleanup accepted")
	}
	if len(f.events) != 1 || f.events[0] != "revoke" {
		t.Fatal("admission/revoke skipped", f.events)
	}
	if err := c.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCollectedReadRetainsOriginalCancellation(t *testing.T) {
	f := &collectorFixture{eof: true}
	c := f.collector()
	ctx, cancel := context.WithCancel(requestContext(t))
	r, err := c.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if b, err := r.Consume(requestContext(t)); b != nil || !errors.Is(err, ErrDenied) {
		t.Fatal("cancelled original request accepted", err)
	}
}
