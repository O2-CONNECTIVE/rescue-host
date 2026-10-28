package operatorauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCancelledFirstAttemptBurnsAndRecoversExistingOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Run("collector", func(t *testing.T) {
		f := &collectorFixture{eof: true}
		c := f.collector()
		if r, err := c.Collect(ctx); r != nil || err == nil {
			t.Fatal("cancelled request accepted")
		}
		if len(f.events) == 0 || f.events[0] != "revoke" {
			t.Fatal("held helper not recovered", f.events)
		}
		if r, err := c.Collect(requestContext(t)); r != nil || !errors.Is(err, ErrDenied) {
			t.Fatal("cancelled collector revived")
		}
	})
	t.Run("helper", func(t *testing.T) {
		h, _, events, _ := helperFixture()
		if err := h.Run(ctx); err == nil {
			t.Fatal("cancelled request accepted")
		}
		if err := h.Run(requestContext(t)); !errors.Is(err, ErrDenied) || len(*events) != 0 {
			t.Fatal("cancelled helper revived", *events)
		}
	})
	t.Run("controller", func(t *testing.T) {
		connected := false
		c := &Controller{cleanup: time.Second, ops: controllerOps{
			connect: func(context.Context) (serverPeer, error) { connected = true; return nil, ErrDenied },
			write:   func(context.Context, []byte) error { return nil },
			read:    func(context.Context) ([]byte, error) { return nil, ErrDenied },
		}}
		if _, err := c.Exchange(ctx, requestFrame()); err == nil {
			t.Fatal("cancelled request accepted")
		}
		if _, err := c.Exchange(requestContext(t), requestFrame()); !errors.Is(err, ErrDenied) || connected {
			t.Fatal("cancelled exchange revived")
		}
	})
}
