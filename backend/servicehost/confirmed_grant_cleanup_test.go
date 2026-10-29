package servicehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

type runtimeCleanupModel struct {
	confirmationRuntimeKernel
	close func(context.Context, *confirmationEvidenceOwner) error
}

func (m *runtimeCleanupModel) CloseOwned(ctx context.Context, owner *confirmationEvidenceOwner) error {
	return m.close(ctx, owner)
}

func TestConfirmedGrantGuestOwnerJoinsAfterGrantBeforeWorkloadAuthAndPlane(t *testing.T) {
	var s *Service
	s, a, outer := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
		_, err := ticket.PreviewFor(s)
		return err
	})
	source := grantRuntimeFixture(t, s)
	entered, release, earlyPlane := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var guestClosed atomic.Bool
	t.Cleanup(func() { guestClosed.Store(true); unblock() })
	source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: source.kernel, close: func(ctx context.Context, owner *confirmationEvidenceOwner) error {
		if owner != source.owner || !s.issuer.Snapshot().Closed {
			t.Error("cleanup lost owner or preceded canonical revocation")
		}
		s.mu.Lock()
		grantFinished := s.preview.intent.confirmation.finished && s.reservation.finished
		confirmed, _ := s.authWriter.WorkloadLocked()
		s.mu.Unlock()
		if !grantFinished {
			t.Error("guest owner cleanup preceded actual grant processing return")
		}
		if confirmed {
			t.Error("whole-workload ack preceded guest resource cleanup")
		}
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
			guestClosed.Store(true)
			return nil
		case <-ctx.Done():
			return ErrRuntimeUnconfirmed
		}
	}}
	hello := previewExchange(t, a, dispatchHello)
	prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	approved := previewExchange(t, a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
	if approved.Approval == nil {
		t.Fatal("grant fixture did not issue")
	}
	// Existing private Witness seam: its lease cannot close before the guest's
	// original resource owner has joined. This models no actual Windows lease.
	s.control.witness.close = func(context.Context) error {
		if !guestClosed.Load() {
			select {
			case earlyPlane <- struct{}{}:
			default:
			}
			return ErrRuntimeUnconfirmed
		}
		s.mu.Lock()
		active := s.auth.active
		s.mu.Unlock()
		select {
		case <-active.ownerDone:
		default:
			t.Error("Plane closed before actual auth worker join")
		}
		return nil
	}
	s.requestStop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-earlyPlane:
		t.Fatal("Plane cleanup ran before guest owner cleanup")
	case <-ctx.Done():
		t.Fatal("guest cleanup did not enter")
	}
	short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := s.Close(short)
	end()
	if !errors.Is(err, ErrRuntimeUnconfirmed) || s.Snapshot().CleanupConfirmed || outer.closed.Load() != 0 {
		t.Fatal("held guest cleanup became overall acknowledgement", err)
	}
	short, end = context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = s.authOwner.AwaitWorkload(short)
	end()
	if err == nil {
		t.Fatal("held guest cleanup became whole-workload acknowledgement")
	}
	unblock()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !guestClosed.Load() || !s.Snapshot().CleanupConfirmed || outer.closed.Load() != 1 {
		t.Fatal("same guest owner did not join before final outer acknowledgement")
	}
}
