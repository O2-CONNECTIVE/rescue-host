package servicehost

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func TestRuntimeGuestChangedRegistrationCannotEnterOwnerRPC(t *testing.T) {
	var s *Service
	var o *runtimeGuestOwnerFixture
	s, o = runtimeGuestServiceFixture(t, func(ctx context.Context, ticket PreviewConfirmationTicket) error {
		s.mu.Lock()
		a := s.preview.runtimeGuest
		original := a.source.kernel
		a.source.kernel = runtimeKernelModel(func(context.Context, PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
			return nil, ErrProvisioningRejected
		})
		s.mu.Unlock()
		err := a.PrepareTarget(ctx, ticket)
		s.mu.Lock()
		a.source.kernel = original
		s.mu.Unlock()
		if err == nil || o.prepares.Load() != 0 {
			t.Error("changed source entered the original owner RPC")
		}
		return ErrProvisioningRejected
	}, runtimeGuestKernelFixture{lease: &serviceready.InspectionLease{}})
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("changed runtime source issued authority")
	}
}

// Post-implementation regression checks of the same public confirmation seam.
func TestRuntimeGuestUnboundForeignOrUnprotectedMaterialCannotIssue(t *testing.T) {
	const sid = "S-1-5-21-7-8-9-2048"
	for _, mode := range []string{"unbound", "foreign-observation", "unprotected"} {
		t.Run(mode, func(t *testing.T) {
			material := &runtimeGuestTargetFixture{sid: sid, protected: mode != "unprotected"}
			if mode == "foreign-observation" {
				material.sid = "S-1-5-21-7-8-9-2049"
			}
			s, o := runtimeGuestServiceFixture(t, nil, runtimeGuestKernelFixture{lease: &serviceready.InspectionLease{}, target: material})
			o.prepare = func(ctx context.Context, ticket RuntimeGuestTicket, _ *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
				d, err := ticket.DescriptorFor(ctx, o)
				if err != nil {
					return nil, childjob.GuestTokenExpectation{}, err
				}
				if mode != "unbound" {
					err = ticket.BindCreatedGuest(ctx, o, d.Namespace, sid)
				}
				return &childjob.Prepared{}, childjob.GuestTokenExpectation{}, err
			}
			hello := previewExchange(t, s.auth, dispatchHello)
			intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, end := context.WithTimeout(context.Background(), time.Second)
			defer end()
			if err := s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil }); err == nil {
				t.Fatal("invalid material reached issuance")
			}
			if s.issuer.Snapshot().Generation != 0 || material.commits.Load() != 0 || s.preview.runtimeGuest.material.Load() == nil {
				t.Fatal("invalid material issued or lost partial ownership")
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if material.closes.Load() != 1 || o.closes.Load() != 1 {
				t.Fatal("invalid material lost the original cleanup owner")
			}
		})
	}
}

func TestRuntimeGuestConcretePathKeepsMissingPhysicalEndpointClosed(t *testing.T) {
	s, o := runtimeGuestServiceFixture(t, nil)
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("missing physical endpoint opened runtime preparation")
	}
	if o.prepares.Load() != 0 || o.closes.Load() != 0 || s.preview.runtimeGuest.record != nil || s.issuer.Snapshot().Generation != 0 {
		t.Fatal("memory authentication or absent endpoint acquired native runtime")
	}
}

func TestRuntimeGuestLatePreparedKeepsOriginalOwnerAndRevocation(t *testing.T) {
	const sid = "S-1-5-21-7-8-9-2048"
	material := &runtimeGuestTargetFixture{sid: sid, protected: true}
	s, o := runtimeGuestServiceFixture(t, nil, runtimeGuestKernelFixture{lease: &serviceready.InspectionLease{}, target: material})
	partial := &childjob.Prepared{}
	o.prepare = func(ctx context.Context, ticket RuntimeGuestTicket, _ *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
		d, err := ticket.DescriptorFor(ctx, o)
		if err != nil {
			return nil, childjob.GuestTokenExpectation{}, err
		}
		a := s.preview.runtimeGuest
		if err := a.PrepareTarget(ctx, PreviewConfirmationTicket{record: ticket.record.confirmation}); err == nil {
			t.Error("same actor materialized twice")
		}
		copy := &runtimeGuestActor{self: a, service: s, owner: o, kernel: a.kernel, source: a.source}
		if err := copy.PrepareTarget(ctx, PreviewConfirmationTicket{record: ticket.record.confirmation}); err == nil {
			t.Error("copied actor materialized")
		}
		s.requestStop() // the stop latch wins before the material reply is published
		if err := ticket.BindCreatedGuest(ctx, o, d.Namespace, sid); err != nil {
			t.Error("late real receipt was lost", err)
		}
		return partial, childjob.GuestTokenExpectation{}, nil
	}
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.auth.exchangeOps(ctx, fixtureAuthBatch(s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("late material became a success")
	}
	if !material.revoked.Load() || material.inspections.Load() != 0 || s.preview.runtimeGuest.material.Load().prepared != partial || s.issuer.Snapshot().Generation != 0 {
		t.Error("late Prepared lost original revocation or was inspected/issued")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if material.closes.Load() != 1 || o.closes.Load() != 1 {
		t.Fatal("late partial ownership was discarded")
	}
}
