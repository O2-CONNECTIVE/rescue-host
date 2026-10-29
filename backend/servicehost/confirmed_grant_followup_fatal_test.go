package servicehost

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

func TestConfirmedGrantGuestFatalNotifiesWaitingAuthWithoutSuccessAck(t *testing.T) {
	var s *Service
	s, a, outer := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
		_, err := ticket.PreviewFor(s)
		return err
	})
	source := grantRuntimeFixture(t, s)
	var guestCloses atomic.Int32
	attempted := make(chan struct{})
	source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: source.kernel, close: func(context.Context, *confirmationEvidenceOwner) error {
		guestCloses.Add(1)
		close(attempted)
		return operatorauth.ErrFatal
	}}
	hello := previewExchange(t, a, dispatchHello)
	prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	_ = previewExchange(t, a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
	batch := fixtureAuthBatch(a, dispatchRequest(hello, 3, "44444444444444444444444444444444", servicecontrol.Status))
	var abort atomic.Bool
	batch.close = func(ctx context.Context) error {
		if abort.Load() {
			return operatorauth.ErrFatal
		}
		return s.authOwner.AwaitWorkload(ctx)
	}
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		result <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { return ErrRuntimeUnconfirmed })
	}()
	t.Cleanup(func() {
		// A failed RED retains a memory worker. Retire it through its existing
		// cleanup request with fatal; never create a successful workload proof.
		abort.Store(true)
		cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		_ = s.waitAuthRequest(cleanup)
		_ = s.Close(cleanup)
	})
	select {
	case <-attempted:
	case <-ctx.Done():
		t.Fatal("guest fatal was not attempted")
	}
	if err := s.authOwner.AwaitWorkload(ctx); !errors.Is(err, operatorauth.ErrFatal) {
		t.Fatal("resource fatal did not wake the same auth owner", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, operatorauth.ErrFatal) {
			t.Fatal("auth worker lost fatal classification", err)
		}
	case <-ctx.Done():
		t.Fatal("auth owner did not retire on guest fatal")
	}
	cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	if err := s.Close(cleanup); !errors.Is(err, operatorauth.ErrFatal) {
		t.Fatal(err)
	}
	if err := s.Close(cleanup); !errors.Is(err, operatorauth.ErrFatal) {
		t.Fatal(err)
	}
	s.mu.Lock()
	confirmed, _ := s.authWriter.WorkloadLocked()
	actorConfirmed := s.preview.intent.confirmation.runtimeCleanupConfirmed
	s.mu.Unlock()
	if confirmed || actorConfirmed || s.Snapshot().CleanupConfirmed || outer.closed.Load() != 0 || guestCloses.Load() != 1 || !s.issuer.Snapshot().Closed {
		t.Fatal("fatal cleanup retried or became a success acknowledgement")
	}
}
