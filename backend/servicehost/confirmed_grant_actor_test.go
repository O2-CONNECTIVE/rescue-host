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

func TestConfirmedGrantNilOrPanickedAcquireStillRequiresRegisteredActorCleanup(t *testing.T) {
	for _, kind := range []string{"nil-error", "nil-nil", "panic"} {
		t.Run(kind, func(t *testing.T) {
			var s *Service
			s, a, outer := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
				_, err := ticket.PreviewFor(s)
				return err
			})
			source := grantRuntimeFixture(t, s)
			kernel := source.kernel
			entered, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var actorHeld, actorClosed atomic.Bool
			source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: runtimeKernelModel(func(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
				_, err := kernel.Acquire(ctx, ticket) // the actor holds resources even when no ref is returned
				if err != nil {
					return nil, err
				}
				actorHeld.Store(true)
				if kind == "panic" {
					panic("private acquisition diagnostic")
				}
				if kind == "nil-error" {
					return nil, errors.New("private acquisition diagnostic")
				}
				return nil, nil
			}), close: func(ctx context.Context, owner *confirmationEvidenceOwner) error {
				if owner != source.owner || !actorHeld.Load() || !s.issuer.Snapshot().Closed {
					t.Error("nil result lost original actor or canonical revoke")
				}
				once.Do(func() { close(entered) })
				select {
				case <-release:
					actorClosed.Store(true)
					return nil
				case <-ctx.Done():
					return ErrRuntimeUnconfirmed
				}
			}}
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			writes := 0
			if err := a.exchangeOps(ctx, fixtureAuthBatch(a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { writes++; return nil }); err == nil {
				t.Fatal("nil/panicked acquisition succeeded")
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("nil result bypassed registered actor cleanup")
			}
			short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
			err := s.Close(short)
			end()
			s.mu.Lock()
			r := s.preview.intent.confirmation
			confirmed, _ := s.authWriter.WorkloadLocked()
			retained := r.runtimeProof == nil && r.runtimeOwner == source.owner && !r.runtimeCleanupConfirmed
			s.mu.Unlock()
			if !errors.Is(err, ErrRuntimeUnconfirmed) || !retained || confirmed || s.Snapshot().CleanupConfirmed || outer.closed.Load() != 0 {
				t.Fatal("unknown actor cleanup was acknowledged", err)
			}
			unblock()
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if !actorClosed.Load() || writes != 0 || s.issuer.Snapshot().Generation != 0 || !s.issuer.Snapshot().Closed || outer.closed.Load() != 1 {
				t.Fatal("same actor did not close without issuing")
			}
		})
	}
}

func TestConfirmedGrantFatalResourceCleanupRetiresWithoutNativeRetry(t *testing.T) {
	var s *Service
	s, a, outer := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
		_, err := ticket.PreviewFor(s)
		return err
	})
	source := grantRuntimeFixture(t, s)
	var calls atomic.Int32
	source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: source.kernel, close: func(context.Context, *confirmationEvidenceOwner) error { calls.Add(1); return operatorauth.ErrFatal }}
	hello := previewExchange(t, a, dispatchHello)
	prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	_ = previewExchange(t, a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, operatorauth.ErrFatal) {
		t.Fatal("resource fatal lost classification", err)
	}
	if err := s.Close(ctx); !errors.Is(err, operatorauth.ErrFatal) {
		t.Fatal("resource fatal recovered", err)
	}
	if calls.Load() != 1 || s.Snapshot().CleanupConfirmed || outer.closed.Load() != 0 || !s.issuer.Snapshot().Closed {
		t.Fatal("fatal cleanup retried or produced cleanup proof")
	}
}
