package guestaccount

import (
	"context"
	"errors"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

type neverCreatedTicketFixture struct {
	revoked, confirmed int
	ns                 servicehost.GuestStagingNamespace
	sid                string
	err                error
}

func (f *neverCreatedTicketFixture) RevokeNeverCreated(_ context.Context, _ servicehost.GuestStagingOwner, ns servicehost.GuestStagingNamespace, sid string) error {
	f.revoked++
	f.ns = ns
	f.sid = sid
	return f.err
}
func (f *neverCreatedTicketFixture) ConfirmNeverCreated(_ context.Context, _ servicehost.GuestStagingOwner, ns servicehost.GuestStagingNamespace, sid string) error {
	f.confirmed++
	f.ns = ns
	f.sid = sid
	return f.err
}

func TestNeverCreatedSessionUsesCreatorSealAndActualOwnerChecks(t *testing.T) {
	p, _ := stagingProfileFixture(t)
	ticket := &neverCreatedTicketFixture{}
	ns := servicehost.GuestStagingNamespace{Epoch: p.binding.epoch, Intent: p.binding.intent, Generation: p.binding.generation}
	sessionSeal := &seal{91}
	s := newNeverCreatedSession(p, ns, ticket, nil, sessionSeal)
	accountSeal := &seal{92}
	made := p.binding
	made.sid = made.machine + "-1002"
	if err := s.bindCreated(accountReceipt{accountSeal, made, created}, accountSeal); err != nil {
		t.Fatal(err)
	}
	if r, e := s.revoke(testContext(t), made); e != nil || r.seal != sessionSeal || r.binding != made || r.kind != revoked {
		t.Fatal("actual revoke receipt", r, e)
	}
	if r, e := s.quiesce(testContext(t), made); e != nil || r.seal != sessionSeal || r.kind != quiescent {
		t.Fatal("actual quiesce receipt", r, e)
	}
	if ticket.revoked != 1 || ticket.confirmed != 1 || ticket.ns != ns || ticket.sid != made.sid {
		t.Fatal("same owner/namespace/SID not retained")
	}
	foreign := made
	foreign.sid = made.machine + "-1003"
	if _, e := s.revoke(testContext(t), foreign); e == nil {
		t.Fatal("foreign SID accepted")
	}
	if e := s.bindCreated(accountReceipt{accountSeal, foreign, created}, accountSeal); e == nil {
		t.Fatal("creator rebound")
	}
	ticket.err = errors.New("owner cleanup pending")
	if r, e := s.quiesce(testContext(t), made); e == nil || r.seal != nil {
		t.Fatal("unknown owner made receipt")
	}
}

// The existing bounded kernel models file/account/query calls; this runs the
// actual stager and lifecycle with the new real session producer. Ticket calls
// are modeled here and are tested against the real issuer in servicehost.
func TestNeverCreatedProducerDrivesDisabledStagerAndRetainsUnknownOwner(t *testing.T) {
	p, c, _, k, ops := stagingTest(t)
	ticket := &neverCreatedTicketFixture{}
	ns := servicehost.GuestStagingNamespace{Epoch: p.binding.epoch, Intent: p.binding.intent, Generation: p.binding.generation}
	sessionSeal := &seal{93}
	session := newNeverCreatedSession(p, ns, ticket, nil, sessionSeal)
	owner, err := newStaging(testContext(t), p, c, session, sessionSeal, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.Prepare(testContext(t)); err != nil || owner.lifecycle.state != Prepared {
		t.Fatal("disabled staging did not complete", err)
	}
	made := owner.lifecycle.record.binding
	if made.sid == "" || session.created != made {
		t.Fatal("actual creator receipt missing")
	}
	if err = owner.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if !owner.closed || ticket.revoked != 1 || ticket.confirmed != 1 || ticket.sid != made.sid || ticket.ns != ns || k.scope.closes != 1 || k.journal.closes != 1 {
		t.Fatal("same namespace/created SID cleanup not acknowledged")
	}
	for _, ref := range k.refs {
		if ref.closes != 1 {
			t.Fatal("native ref not joined")
		}
	}
}

func TestNeverCreatedSessionUnknownOwnerNeverBecomesCleanupReceipt(t *testing.T) {
	p, c, _, k, ops := stagingTest(t)
	ticket := &neverCreatedTicketFixture{err: ErrCleanup}
	ns := servicehost.GuestStagingNamespace{Epoch: p.binding.epoch, Intent: p.binding.intent, Generation: p.binding.generation}
	sessionSeal := &seal{94}
	session := newNeverCreatedSession(p, ns, ticket, nil, sessionSeal)
	owner, err := newStaging(testContext(t), p, c, session, sessionSeal, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if err = owner.Close(testContext(t)); err == nil {
		t.Fatal("unknown cleanup acknowledged")
	}
	ticket.err = nil
	if err = owner.Close(testContext(t)); err == nil || owner.closed || k.scope.closes != 0 || k.journal.closes != 0 {
		t.Fatal("unknown outcome erased by later observation", err)
	}
}
