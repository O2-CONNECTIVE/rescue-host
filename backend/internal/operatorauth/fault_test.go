package operatorauth

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHelperRestorationFailureAbortsWithoutReportOrUnlock(t *testing.T) {
	for _, kind := range []string{"revert", "token-close", "token-panic", "post-clean"} {
		t.Run(kind, func(t *testing.T) {
			h, thread, events, published := helperFixture()
			switch kind {
			case "revert":
				thread.revertErr = ErrFatal
			case "token-close":
				thread.tokenErr = ErrFatal
			case "token-panic":
				thread.onToken = func() { panic("synthetic query failure") }
			case "post-clean":
				thread.onToken = func() { thread.cleanErr = ErrFatal }
			}
			if !errors.Is(h.Run(requestContext(t)), ErrFatal) || len(*published) != 0 {
				t.Fatal("fatal published", *events)
			}
			if (*events)[len(*events)-1] != "abort" {
				t.Fatal("fatal reused caller thread", *events)
			}
			if !strings.Contains(strings.Join(*events, ","), "revert") {
				t.Fatal("revert skipped", *events)
			}
		})
	}
}

func TestHelperRequiresExactNativeTokenProjection(t *testing.T) {
	for _, kind := range []string{"guest", "authentication", "session", "integrity", "type", "anonymous", "delegation", "restricted", "app-container"} {
		t.Run(kind, func(t *testing.T) {
			h, thread, _, published := helperFixture()
			switch kind {
			case "guest":
				thread.projection.user = "guest"
			case "authentication":
				thread.projection.authentication++
			case "session":
				thread.projection.session++
			case "integrity":
				thread.projection.integrity++
			case "type":
				thread.projection.kind = 1
			case "anonymous":
				thread.projection.level = 0
			case "delegation":
				thread.projection.level = 3
			case "restricted":
				thread.projection.restricted = true
			case "app-container":
				thread.projection.appContainer = true
			}
			if !errors.Is(h.Run(requestContext(t)), ErrDenied) || len(*published) != 0 {
				t.Fatal("unbound token published")
			}
		})
	}
}

func TestHelperRejectsCancellationDuringFinalObservation(t *testing.T) {
	h, _, _, published := helperFixture()
	ctx, cancel := context.WithCancel(requestContext(t))
	defer cancel()
	checks := 0
	h.ops.check = func(context.Context) error {
		checks++
		if checks == 3 {
			cancel()
		}
		return nil
	}
	if !errors.Is(h.Run(ctx), ErrDenied) || len(*published) != 0 {
		t.Fatal("cancelled report published")
	}
}

func TestHelperRejectsMalformedOrExcessRead(t *testing.T) {
	for _, frame := range [][]byte{nil, []byte("{}\n"), append(requestFrame(), requestFrame()...), []byte(strings.Repeat("x", 16385))} {
		h, _, _, published := helperFixture()
		h.ops.read = func(context.Context) ([]byte, error) { return frame, nil }
		if !errors.Is(h.Run(requestContext(t)), ErrDenied) || len(*published) != 0 {
			t.Fatal("invalid report published")
		}
	}
}

func TestCollectorRequiresEOFExitAndCurrentDeadline(t *testing.T) {
	for _, kind := range []string{"no-eof", "exit", "wait-unknown", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f := &collectorFixture{eof: true}
			ctx, cancel := context.WithCancel(requestContext(t))
			defer cancel()
			switch kind {
			case "no-eof":
				f.eof = false
			case "exit":
				f.exit = 1
			case "wait-unknown":
				f.waitErr = ErrUnconfirmed
			case "cancel":
				f.onWait = cancel
			}
			c := f.collector()
			r, err := c.Collect(ctx)
			if err == nil || r != nil {
				t.Fatal("unconfirmed report accepted")
			}
			if _, err = c.Collect(requestContext(t)); !errors.Is(err, ErrDenied) {
				t.Fatal("failed collector revived")
			}
			if kind == "wait-unknown" {
				for _, v := range f.events {
					if v == "process-close" {
						t.Fatal("unknown process released", f.events)
					}
				}
				f.waitErr = nil
				if err = c.Close(requestContext(t)); err != nil {
					t.Fatal(err)
				}
			}
			joined := strings.Join(f.events, ",")
			if !strings.Contains(joined, "revoke,workload-reclaim") {
				t.Fatal("owner revocation omitted", joined)
			}
		})
	}
}

func TestCollectorCloseInvalidatesUnconsumedRead(t *testing.T) {
	f := &collectorFixture{eof: true}
	c := f.collector()
	r, err := c.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Consume(requestContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked read revived")
	}
	var forged Read
	if _, err = forged.Consume(requestContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("zero proof accepted")
	}
}

func TestCollectorBlockedReportDoesNotOwnWorkloadRecovery(t *testing.T) {
	f := &collectorFixture{eof: true}
	c := f.collector()
	started, release := make(chan struct{}), make(chan struct{})
	var revoked, reclaimed, terminated atomic.Bool
	c.ops.check = func(context.Context) error { return nil }
	c.ops.drain = func(context.Context) (reportResult, error) {
		close(started)
		<-release
		return reportResult{requestFrame(), true}, nil
	}
	c.ops.revoke = func() error { revoked.Store(true); return nil }
	c.ops.reclaim = func(context.Context) error {
		if !revoked.Load() {
			return ErrDenied
		}
		reclaimed.Store(true)
		return nil
	}
	c.ops.terminate = func() error { terminated.Store(true); return nil }
	result := make(chan error, 1)
	go func() { _, e := c.Collect(requestContext(t)); result <- e }()
	<-started
	if !errors.Is(c.Close(requestContext(t)), ErrUnconfirmed) || !revoked.Load() || !reclaimed.Load() || !terminated.Load() {
		t.Fatal("recovery blocked on report")
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("closed collector accepted report")
	}
	if err := c.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestUnconfiguredFactoriesDoNotUseNativeInputs(t *testing.T) {
	if !errors.Is(RunFixed(requestContext(t)), ErrUnconfigured) {
		t.Fatal("helper gate opened")
	}
	if c, err := NewNativeController(); c != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("controller gate opened")
	}
	var h Helper
	if !errors.Is(h.Run(requestContext(t)), ErrDenied) {
		t.Fatal("zero helper accepted")
	}
	var c Collector
	if r, err := c.Collect(requestContext(t)); r != nil || !errors.Is(err, ErrDenied) {
		t.Fatal("zero collector accepted")
	}
}
