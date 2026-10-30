package servicehost

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// This private physical-operation seam models inspection/material only. Its
// synthetic lease and target are never accepted as native Windows evidence.
type runtimeGuestKernelFixture struct {
	lease  *serviceready.InspectionLease
	target runtimeGuestTarget
}

func (k runtimeGuestKernelFixture) inspection(*serviceAuthRequest) *serviceready.InspectionLease {
	return k.lease
}
func (k runtimeGuestKernelFixture) material(*childjob.Prepared, childjob.GuestTokenExpectation) runtimeGuestTarget {
	return k.target
}

type runtimeGuestTargetFixture struct {
	confirmationTargetMemory
	sid       string
	protected bool
}

func (m *runtimeGuestTargetFixture) matchesGuestSID(sid string) bool {
	return m.protected && m.sid == sid && !m.revoked.Load()
}

func TestRuntimeGuestActorMaterializesSameTargetBeforeRealIssuerPrepare(t *testing.T) {
	const sid = "S-1-5-21-7-8-9-2048"
	material := &runtimeGuestTargetFixture{sid: sid, protected: true}
	lease := &serviceready.InspectionLease{}
	s, o := runtimeGuestServiceFixture(t, nil, runtimeGuestKernelFixture{lease: lease, target: material})
	source, owner, target := s.preview.runtime, s.preview.runtime.owner, s.preview.runtime.target
	var saved RuntimeGuestTicket
	o.prepare = func(ctx context.Context, ticket RuntimeGuestTicket, got *serviceready.InspectionLease) (*childjob.Prepared, childjob.GuestTokenExpectation, error) {
		saved = ticket
		if got != lease || material.inspections.Load() != 0 || s.issuer.Snapshot().Generation != 0 {
			t.Error("preparation used another endpoint or followed inspection/issuance")
		}
		d, err := ticket.DescriptorFor(ctx, o)
		if err != nil {
			return nil, childjob.GuestTokenExpectation{}, err
		}
		if err := ticket.BindCreatedGuest(ctx, o, d.Namespace, sid); err != nil {
			return nil, childjob.GuestTokenExpectation{}, err
		}
		e, err := childjob.NewBatchGuestTokenExpectation(sid, []string{"S-1-1-0", "S-1-5-3", "S-1-5-11", "S-1-5-32-545"}, []string{"SeChangeNotifyPrivilege"})
		return &childjob.Prepared{}, e, err // the private child-target seam above is an explicit fake
	}
	hello := previewExchange(t, s.auth, dispatchHello)
	intent := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	approved := previewExchange(t, s.auth, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"))
	if approved.Approval == nil || o.prepares.Load() != 1 || material.inspections.Load() != 1 || material.commits.Load() != 0 || s.issuer.Snapshot().Generation != 1 {
		t.Fatal("runtime actor skipped material/inspection/actual issuer", approved)
	}
	if s.preview.runtime != source || source.owner != owner || source.target != target || s.preview.targetOwner.target != target || saved.record.actor != s.preview.runtimeGuest {
		t.Fatal("materialization replaced the registered actor or target")
	}
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.launchConfirmedTarget(ctx, s.preview.grant); err != nil {
		t.Fatal(err)
	}
	if material.commits.Load() != 1 || !s.issuer.Snapshot().Consumed {
		t.Fatal("actual issuer did not consume the same target")
	}
	if err := s.launchConfirmedTarget(ctx, s.preview.grant); err == nil {
		t.Fatal("runtime target launched twice")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if o.closes.Load() != 1 || material.closes.Load() != 1 || !material.revoked.Load() || !s.Snapshot().CleanupConfirmed {
		t.Fatal("original runtime actor did not close")
	}
	if err := saved.BindCreatedGuest(ctx, o, saved.record.descriptor.Namespace, sid); err == nil {
		t.Fatal("closed runtime accepted a new receipt")
	}
}
