package serviceprovision

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// The only production supplier is the original Prepared owner. The pure value
// is not authentication; no SID is accepted from a message, candidate or cache.
func (p *Prepared) operatorSIDPolicy(ctx context.Context) (operatorauth.SIDPolicy, error) {
	_, record, _, err := p.previewRecord(ctx)
	if err != nil {
		return operatorauth.SIDPolicy{}, err
	}
	return operatorauth.SIDPolicy{OperatorSID: record.Profile.Keys.OperatorSID, GuestSID: record.Profile.Keys.GuestSID}, nil
}

type operatorSIDPolicyStage interface {
	BindOperatorSIDPolicy(context.Context, *servicehost.Service, servicehost.OuterFinalizer) error
}

func (s *nativeExecutionStage) BindOperatorSIDPolicy(ctx context.Context, service *servicehost.Service, owner servicehost.OuterFinalizer) error {
	if s == nil || s.prepared == nil || s.previewSnapshot == nil || service == nil || service != s.service || owner == nil || !rootLive(ctx) {
		return ErrRootDenied
	}
	if err := s.VerifyPreviewConfirmation(ctx, *s.previewSnapshot); err != nil {
		return err
	}
	policy, err := s.prepared.operatorSIDPolicy(ctx)
	if err != nil {
		return err
	}
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	if err = service.BindOperatorSIDPolicy(owner, policy); err != nil {
		return err
	}
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	return nil
}
