package guestaccount

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// Only the actual Service ticket supplies these operations in production. This
// private seam models the OS/owner boundary in memory tests, never authority.
type neverCreatedTicket interface {
	RevokeNeverCreated(context.Context, servicehost.GuestStagingOwner, servicehost.GuestStagingNamespace, string) error
	ConfirmNeverCreated(context.Context, servicehost.GuestStagingOwner, servicehost.GuestStagingNamespace, string) error
}
type neverCreatedSession struct {
	plan      Plan
	namespace servicehost.GuestStagingNamespace
	ticket    neverCreatedTicket
	owner     servicehost.GuestStagingOwner
	seal      *seal
	created   binding
}

func newNeverCreatedSession(p Plan, ns servicehost.GuestStagingNamespace, ticket neverCreatedTicket, owner servicehost.GuestStagingOwner, s *seal) *neverCreatedSession {
	return &neverCreatedSession{plan: p, namespace: ns, ticket: ticket, owner: owner, seal: s}
}
func (s *neverCreatedSession) bindCreated(r accountReceipt, expected *seal) error {
	if s == nil || expected == nil || r.seal != expected || r.kind != created || !s.plan.validBinding(r.binding) {
		return ErrUnknown
	}
	if s.created.sid != "" && s.created != r.binding {
		return ErrUnknown
	}
	s.created = r.binding
	return nil
}
func (s *neverCreatedSession) matches(b binding) bool {
	if s == nil || s.ticket == nil || s.seal == nil {
		return false
	}
	if s.created.sid != "" {
		return b == s.created
	}
	return b == s.plan.binding && b.sid == ""
}
func (s *neverCreatedSession) revoke(ctx context.Context, b binding) (sessionReceipt, error) {
	if !finite(ctx) || !s.matches(b) {
		return sessionReceipt{}, ErrDenied
	}
	if err := s.ticket.RevokeNeverCreated(ctx, s.owner, s.namespace, b.sid); err != nil {
		return sessionReceipt{}, err
	}
	if !finite(ctx) {
		return sessionReceipt{}, ErrCleanup
	}
	return sessionReceipt{s.seal, b, revoked}, nil
}
func (s *neverCreatedSession) quiesce(ctx context.Context, b binding) (sessionReceipt, error) {
	if !finite(ctx) || !s.matches(b) {
		return sessionReceipt{}, ErrDenied
	}
	// Never waits for the global workload/auth/Plane acknowledgements. The same
	// permanently closed execution owner confirms only this never-created target.
	if err := s.ticket.ConfirmNeverCreated(ctx, s.owner, s.namespace, b.sid); err != nil {
		return sessionReceipt{}, err
	}
	if !finite(ctx) {
		return sessionReceipt{}, ErrCleanup
	}
	return sessionReceipt{s.seal, b, quiescent}, nil
}
