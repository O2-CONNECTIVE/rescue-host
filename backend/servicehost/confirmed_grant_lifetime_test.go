package servicehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

func TestConfirmedGrantAcquisitionRetainsPartialOwnerUntilActualReturn(t *testing.T) {
	for _, kind := range []string{"stop", "cancel", "deadline", "late-error", "owner-revoke", "panic"} {
		t.Run(kind, func(t *testing.T) {
			var s *Service
			s, a, outer := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
				_, err := ticket.PreviewFor(s)
				return err
			})
			source := grantRuntimeFixture(t, s)
			kernel := source.kernel
			entered, release := make(chan *confirmationRuntimeProof, 1), make(chan struct{})
			source.kernel = runtimeKernelModel(func(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
				p, err := kernel.Acquire(ctx, ticket)
				if err != nil {
					return p, err
				}
				entered <- p
				<-release
				if kind == "late-error" {
					return p, errors.New("private partial runtime diagnostic")
				}
				if kind == "panic" {
					panic("private runtime panic")
				}
				return p, nil
			})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := make(chan error, 1)
			writes := 0
			go func() {
				result <- a.exchangeOps(ctx, fixtureAuthBatch(a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { writes++; return nil })
			}()
			var partial *confirmationRuntimeProof
			select {
			case partial = <-entered:
			case <-ctx.Done():
				t.Fatal("runtime acquisition did not enter its memory barrier")
			}
			s.mu.Lock()
			r := s.preview.intent.confirmation
			registered := s.reservation == r.work.reservation && s.reservation.claimed && !s.reservation.finished && !r.finished && r.runtimeOwner == source.owner
			worker := s.auth.active
			s.mu.Unlock()
			if !registered {
				t.Fatal("native acquisition preceded actual reservation/owner registration")
			}
			switch kind {
			case "stop":
				s.requestStop()
			case "cancel":
				cancel()
			case "deadline":
				<-ctx.Done()
			case "owner-revoke":
				if err := source.owner.revoke(); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "late-error" && kind != "panic" {
				short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
				err := s.Close(short)
				end()
				if !errors.Is(err, ErrRuntimeUnconfirmed) || s.Snapshot().CleanupConfirmed || outer.closed.Load() != 0 {
					t.Fatal("pending runtime owner was acknowledged as physically cleaned", err)
				}
			}
			select {
			case <-worker.ownerDone:
				t.Fatal("auth owner returned before acquisition")
			default:
			}
			select {
			case <-s.reservation.done:
				t.Fatal("reservation finished before acquisition return")
			default:
			}
			unblock()
			join, end := context.WithTimeout(context.Background(), 2*time.Second)
			defer end()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("late/failed runtime acquisition succeeded")
				}
			case <-join.Done():
				t.Fatal("runtime owner did not join")
			}
			if err := s.Close(join); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			retained := r.runtimeOwner == source.owner && r.finished && s.reservation.finished && (kind == "panic" || r.runtimeProof == partial && r.partialOwner == partial.owner)
			s.mu.Unlock()
			_, held := source.owner.proofs[partial]
			if !retained || !held || partial.consumed || writes != 0 || !s.issuer.Snapshot().Closed || s.issuer.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
				t.Fatal("partial runtime proof was lost, consumed, or issued after failure")
			}
		})
	}
}
