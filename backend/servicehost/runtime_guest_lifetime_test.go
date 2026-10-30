package servicehost

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func TestRuntimeGuestErrorReplyKeepsInspectionAndLateReceiptUntilActualClose(t *testing.T) {
	lease := &serviceready.InspectionLease{}
	s, o := runtimeGuestServiceFixture(t, nil, runtimeGuestKernelFixture{lease: lease})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	defer finish()
	var ticket RuntimeGuestTicket
	var request *serviceAuthRequest
	var inspectionCloses atomic.Int32
	o.prepare = func(_ context.Context, got RuntimeGuestTicket, _ *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
		ticket, request = got, got.record.confirmation.request
		s.mu.Lock()
		request.endpoint = &serviceAuthEndpoint{inspection: lease, inspectionClose: func(context.Context) error { inspectionCloses.Add(1); return nil }}
		s.mu.Unlock()
		return nil, childjob.GuestTokenExpectation{}, context.Canceled // RPC returned; modeled original native worker has not
	}
	o.closeGuest = func(context.Context, RuntimeGuestTicket, *serviceready.InspectionLease) error {
		close(entered)
		<-release // this explicit fake models actual Close acknowledgement
		return nil
	}
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	result := make(chan error, 1)
	go func() {
		result <- s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil })
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original runtime close did not enter")
	}
	if err := request.closeEndpoint(ctx); err == nil || inspectionCloses.Load() != 0 {
		t.Error("error RPC reply released a still-borrowed inspection", err)
	}
	s.mu.Lock()
	finished := ticket.record.confirmation.finished
	ns := ticket.record.descriptor.Namespace
	originalOwner := s.preview.runtime.owner
	s.preview.runtime.owner = &confirmationEvidenceOwner{} // mutable report cannot retarget late receipt/cleanup
	s.mu.Unlock()
	if !finished || s.Snapshot().CleanupConfirmed {
		t.Error("native join was confused with confirmation or whole cleanup")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	const sid = "S-1-5-21-7-8-9-2048"
	if err := ticket.BindCreatedGuest(canceled, o, ns, sid); err != nil {
		t.Error("late actual creation receipt lost after confirmation ended", err)
	}
	if err := ticket.BindCreatedGuest(canceled, &runtimeGuestOwnerFixture{service: s}, ns, sid); err == nil {
		t.Error("foreign owner bound late receipt")
	}
	if err := ticket.RevokeRuntimeGuest(ctx, o, ns, sid); err != nil {
		t.Error(err)
	}
	s.mu.Lock()
	s.preview.runtime.owner = originalOwner
	s.mu.Unlock()
	if actual := s.issuer.Snapshot(); !actual.Closed || actual.Generation != 0 || actual.HasAuthority {
		t.Error("receipt or revocation manufactured runtime authority")
	}
	finish()
	select {
	case err := <-result:
		if err == nil {
			t.Error("failed preparation became success")
		}
	case <-ctx.Done():
		t.Fatal("original auth worker did not join")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.Snapshot().CleanupConfirmed || inspectionCloses.Load() != 1 {
		t.Fatal("original owner did not release inspection exactly once")
	}
	if err := ticket.BindCreatedGuest(canceled, o, ns, sid); err == nil {
		t.Fatal("closed runtime accepted another receipt")
	}
}
