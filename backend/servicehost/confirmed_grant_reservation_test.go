package servicehost

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// Err commits cancellation at an actual reservation boundary, without a timer
// race or a production callback. Done is closed before Err returns Canceled,
// and Deadline remains the original finite request deadline. These state reads
// occur on the auth worker while it owns Service.mu; no actor has started yet.
type confirmationReservationCancelContext struct {
	context.Context
	service *Service
	stage   string
	cancel  context.CancelFunc
	fired   atomic.Bool
}

func (c *confirmationReservationCancelContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	if !c.fired.Load() && c.service.ownerIntent != nil {
		r := c.service.reservation
		if c.stage == "reserve" && r == nil || c.stage == "consume" && r != nil && !r.claimed {
			if c.fired.CompareAndSwap(false, true) {
				c.cancel()
			}
		}
	}
	return c.Context.Err()
}

func TestConfirmedGrantCancellationBeforeReservationClaimKeepsStaticSource(t *testing.T) {
	for _, stage := range []string{"reserve", "consume"} {
		t.Run(stage, func(t *testing.T) {
			var records, acquires, actorCloses atomic.Int32
			s, a, outer := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { records.Add(1); return nil })
			source := grantRuntimeFixture(t, s)
			owner := source.owner
			source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: runtimeKernelModel(func(context.Context, PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
				acquires.Add(1)
				return nil, ErrRuntimeUnconfirmed
			}), close: func(context.Context, *confirmationEvidenceOwner) error { actorCloses.Add(1); return nil }}
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			base, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx := &confirmationReservationCancelContext{Context: base, service: s, stage: stage, cancel: cancel}
			batch := fixtureAuthBatch(a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
			var abort atomic.Bool
			var closes atomic.Int32
			batch.close = func(ctx context.Context) error {
				closes.Add(1)
				if abort.Load() {
					return operatorauth.ErrFatal
				}
				return s.authOwner.AwaitWorkload(ctx)
			}
			result, joined := make(chan error, 1), make(chan struct{})
			var writes atomic.Int32
			go func() {
				defer close(joined)
				result <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { writes.Add(1); return nil })
			}()
			t.Cleanup(func() {
				// RED teardown retires a retained memory worker with fatal rather
				// than fabricating the workload or actor cleanup acknowledgement.
				abort.Store(true)
				cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
				defer end()
				_ = s.waitAuthRequest(cleanup)
				_ = s.Close(cleanup)
				select {
				case <-joined:
				case <-cleanup.Done():
					t.Error("reservation cancellation worker did not join")
				}
			})
			cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
			defer end()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled reservation succeeded")
				}
			case <-cleanup.Done():
				t.Fatal("reservation refusal retained actor responsibility before claim")
			}
			if err := s.Close(cleanup); err != nil {
				t.Fatal("pre-acquisition cancellation could not terminate", err)
			}
			s.mu.Lock()
			r := s.preview.intent.confirmation
			kept := s.preview.runtime == source && source.owner == owner && !r.runtimeCleanupConfirmed
			reservation := s.reservation
			s.mu.Unlock()
			if !ctx.fired.Load() || records.Load() != 0 || acquires.Load() != 0 || actorCloses.Load() != 0 || writes.Load() != 0 || closes.Load() == 0 || !kept || outer.closed.Load() != 1 || !s.Snapshot().CleanupConfirmed || s.Snapshot().Generation != 0 || !s.issuer.Snapshot().Closed {
				t.Fatal("pre-acquisition refusal lost ownership or invented actor cleanup")
			}
			if stage == "reserve" && reservation != nil || stage == "consume" && (reservation == nil || reservation.claimed || !reservation.finished || !reservation.canceled) {
				t.Fatal("wrong reservation cancellation boundary")
			}
		})
	}
}
