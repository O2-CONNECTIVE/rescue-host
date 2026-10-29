package servicehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

type inspectionBorrowOwner struct {
	guestOwnerFixture
	entered, release chan struct{}
}

func (o *inspectionBorrowOwner) PrepareNeverCreatedGuest(ctx context.Context, _ GuestStagingTicket, _ *serviceready.InspectionLease) error {
	close(o.entered)
	select {
	case <-o.release:
		return nil
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
}

// Only native SDK/physical-close operations are modeled. No opaque Endpoint or
// authenticated Read receipt is manufactured and this never admits an exchange.
func TestGuestInspectionSDKJoinAndPhysicalReturnPrecedeEndpointWithoutGuestTeardown(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lease := new(serviceready.InspectionLease)
	var physical, endpoint atomic.Int32
	var cleanupUnknown atomic.Bool
	cleanupUnknown.Store(true)
	r.endpoint = &serviceAuthEndpoint{inspection: lease, ops: serviceAuthEndpointOps{concrete: new(serviceready.Endpoint), close: func(context.Context) error { endpoint.Add(1); return nil }}, inspectionClose: func(context.Context) error {
		physical.Add(1)
		if cleanupUnknown.Load() {
			return ErrRuntimeUnconfirmed
		}
		return nil
	}}
	owner := &inspectionBorrowOwner{entered: make(chan struct{}), release: make(chan struct{})}
	stage := &guestStagingRecord{service: s, owner: owner, lease: lease, transferred: true}
	s.staging = stage
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(owner.release) }) }
	defer release()
	joined := make(chan error, 1)
	go func() { joined <- stage.prepare(ctx) }()
	select {
	case <-owner.entered:
	case <-ctx.Done():
		t.Fatal("SDK did not start")
	}
	if err = r.closeEndpoint(ctx); !errors.Is(err, ErrRuntimeUnconfirmed) || physical.Load() != 0 || endpoint.Load() != 0 {
		t.Fatal("caller closed a borrowed inspection before SDK return", err)
	}
	release()
	select {
	case err = <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SDK did not join")
	}
	if err = r.closeEndpoint(ctx); err == nil || physical.Load() != 1 || endpoint.Load() != 0 || r.endpoint.inspectionReturned {
		t.Fatal("after SDK actual return, physical close must run and remain unknown", err, physical.Load(), endpoint.Load())
	}
	cleanupUnknown.Store(false)
	if err = r.closeEndpoint(ctx); err != nil || physical.Load() != 2 || endpoint.Load() != 1 || !r.endpoint.inspectionReturned || stage.closed {
		t.Fatal("short lease return required whole guest teardown", err)
	}
	if err = r.closeEndpoint(ctx); err != nil || physical.Load() != 2 || endpoint.Load() != 1 {
		t.Fatal("confirmed physical close repeated native work", err)
	}
}

func TestGuestInspectionCanceledPrepareDoesNotReturnBorrow(t *testing.T) {
	s, a := authUnstartedMemoryOwner(t)
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	r, err := a.reserveExchange(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	lease := new(serviceready.InspectionLease)
	var physical atomic.Int32
	r.endpoint = &serviceAuthEndpoint{inspection: lease, inspectionClose: func(context.Context) error { physical.Add(1); return nil }}
	owner := &inspectionBorrowOwner{entered: make(chan struct{}), release: make(chan struct{})}
	stage := &guestStagingRecord{service: s, owner: owner, lease: lease, transferred: true}
	s.staging = stage
	request, cancel := context.WithCancel(cleanup)
	defer cancel()
	joined := make(chan error, 1)
	go func() { joined <- stage.prepare(request) }()
	select {
	case <-owner.entered:
	case <-cleanup.Done():
		t.Fatal("SDK did not start")
	}
	cancel()
	select {
	case err = <-joined:
		if !errors.Is(err, ErrRuntimeUnconfirmed) {
			t.Fatal("canceled prepare acknowledged success", err)
		}
	case <-cleanup.Done():
		t.Fatal("canceled SDK did not return")
	}
	if stage.prepareJoined || r.closeEndpoint(cleanup) == nil || physical.Load() != 0 || r.endpoint.inspectionReturned {
		t.Fatal("cancellation returned the transferred inspection borrow")
	}
}
