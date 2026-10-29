package operatorauth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// cleanupにも有限deadlineを要求する。既取消contextは一度のpollには使える。
func boundedCleanup(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errReportInvalid
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, nil, errReportInvalid
	}
	if end := time.Now().Add(budget); end.Before(deadline) {
		deadline = end
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	return bounded, cancel, nil
}
func (o *reportOwner) admitCleanup(ctx context.Context) error {
	for {
		if o.cleanup.TryLock() {
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(ErrUnconfirmed, ctx.Err())
		}
		deadline, _ := ctx.Deadline()
		delay := time.Millisecond
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			return errors.Join(ErrUnconfirmed, context.DeadlineExceeded)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ErrUnconfirmed, ctx.Err())
		case <-timer.C:
		}
	}
}
func reportContextValid(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Now().Before(deadline)
}
func reportSIDValid(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 4 || len(parts) > 18 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for i, p := range parts[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, e := strconv.ParseUint(p, 10, bits)
		if e != nil || strconv.FormatUint(n, 10) != p {
			return false
		}
	}
	return true
}
