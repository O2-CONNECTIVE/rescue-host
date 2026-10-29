package servicehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

func TestPreviewConfirmationRetainsWorkerUntilLateResolverActuallyReturns(t *testing.T) {
	for _, kind := range []string{"stop", "cancel", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			entered, release := make(chan PreviewConfirmationTicket, 1), make(chan struct{})
			s, a, owner := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
				entered <- ticket
				<-release // bounded by the test's release and worker-join cleanup
				return nil
			})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var writes atomic.Int32
			result := make(chan error, 1)
			go func() {
				result <- a.exchangeOps(ctx, fixtureAuthBatch(a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { writes.Add(1); return nil })
			}()
			var ticket PreviewConfirmationTicket
			select {
			case ticket = <-entered:
			case <-ctx.Done():
				t.Fatal("resolver did not enter the memory barrier")
			}
			switch kind {
			case "stop":
				s.requestStop()
			case "cancel":
				cancel()
			case "deadline":
				<-ctx.Done()
			}
			if _, err := ticket.PreviewFor(s); err == nil {
				t.Fatal("stopped or expired resolver retained a live ticket")
			}
			short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
			err := s.Close(short)
			end()
			if !errors.Is(err, ErrRuntimeUnconfirmed) || s.Snapshot().CleanupConfirmed || owner.closed.Load() != 0 {
				t.Fatal("held resolver became a cleanup acknowledgement", err)
			}
			s.mu.Lock()
			worker := s.auth.active
			finished := ticket.record.finished
			s.mu.Unlock()
			if finished {
				t.Fatal("confirmation finished before resolver return")
			}
			select {
			case <-worker.ownerDone:
				t.Fatal("auth worker retired while resolver was held")
			default:
			}
			unblock()
			join, joined := context.WithTimeout(context.Background(), 2*time.Second)
			defer joined()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("late successful resolver result escaped cancellation")
				}
			case <-join.Done():
				t.Fatal("resolver worker did not join")
			}
			if err := s.Close(join); err != nil {
				t.Fatal(err)
			}
			if writes.Load() != 0 || s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority || owner.closed.Load() != 1 {
				t.Fatal("late resolver wrote a response, gained authority, or lost outer ownership")
			}
		})
	}
}

func TestPreviewConfirmationFailureAndPanicDoNotWriteOrGrant(t *testing.T) {
	for _, kind := range []string{"error", "panic", "fatal"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error {
				switch kind {
				case "panic":
					panic("private owner diagnostic")
				case "fatal":
					return operatorauth.ErrFatal
				default:
					return errors.New("private owner diagnostic")
				}
			})
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			writes, closes := 0, 0
			batch := fixtureAuthBatch(a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
			batch.close = func(context.Context) error { closes++; return nil }
			err := a.exchangeOps(ctx, batch, func(context.Context, []byte) error { writes++; return nil })
			if err == nil || errors.Is(err, operatorauth.ErrFatal) != (kind == "fatal") || kind == "fatal" && closes != 0 || writes != 0 || !s.Snapshot().Stopping || s.Snapshot().HasAuthority || s.Snapshot().Generation != 0 {
				t.Fatal("resolver failure lost classification or gained authority", err)
			}
		})
	}
}
