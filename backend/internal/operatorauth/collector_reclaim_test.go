package operatorauth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

func TestCollectorReclaimFatalRetainsClassificationAndCleansIndependentHelper(t *testing.T) {
	for _, fatal := range []error{ErrFatal, bootstrapauth.ErrFatal, protectedkey.ErrFatal} {
		t.Run(fatal.Error(), func(t *testing.T) {
			f := &collectorFixture{reclaimErr: errors.Join(fatal, errors.New("private callback detail")), ioErr: ErrUnconfirmed}
			c := f.collector()
			err := c.Close(requestContext(t))
			if !errors.Is(err, fatal) || !errors.Is(err, ErrUnconfirmed) || strings.Contains(err.Error(), "private callback detail") {
				t.Fatalf("lost terminal classification or exposed callback detail: %v", err)
			}
			if c.reclaimed || !c.waited || c.ioClosed || c.processClosed || slices.Index(f.events, "io-close-join") < 0 || slices.Index(f.events, "workload-reclaim") > slices.Index(f.events, "wait") {
				t.Fatal("fatal workload skipped independent helper cleanup or became success", f.events)
			}
			f.reclaimErr = nil
			before := len(f.events)
			if err := c.Close(requestContext(t)); !errors.Is(err, fatal) || len(f.events) != before {
				t.Fatal("terminal workload retried native callbacks or lost fatal", f.events, err)
			}
		})
	}
}

func TestCollectorReclaimTimeoutPreservesIndependentHelperBudget(t *testing.T) {
	f := &collectorFixture{}
	c := f.collector()
	c.ops.reclaim = func(ctx context.Context) error { <-ctx.Done(); return ErrUnconfirmed }
	wait := c.ops.wait
	c.ops.wait = func(ctx context.Context) (uint32, error) {
		if !reportContextValid(ctx) {
			return 0, ErrUnconfirmed
		}
		return wait(ctx)
	}
	closeIO := c.ops.closeIO
	c.ops.closeIO = func(ctx context.Context) error {
		if !reportContextValid(ctx) {
			return ErrUnconfirmed
		}
		return closeIO(ctx)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if !c.waited || !c.ioClosed || !c.processClosed || c.reclaimed {
		t.Fatal("workload timeout exhausted independent helper cleanup budget", f.events)
	}
}

func TestCollectorUnknownReclaimRetainsOwnerForRetryAfterHelperCleanup(t *testing.T) {
	f := &collectorFixture{reclaimErr: errors.New("private unknown detail")}
	c := f.collector()
	if err := c.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) || strings.Contains(err.Error(), "private unknown detail") {
		t.Fatal(err)
	}
	if c.reclaimed || !c.waited || !c.ioClosed || !c.processClosed {
		t.Fatal("unknown workload skipped independent helper cleanup or became success")
	}
	f.reclaimErr = nil
	before := len(f.events)
	if err := c.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if !c.reclaimed || len(f.events) != before+1 || f.events[before] != "workload-reclaim" {
		t.Fatal("retry replaced owner or repeated completed helper cleanup", f.events)
	}
	if c.proof != nil {
		t.Fatal("cleanup manufactured authentication")
	}
}
