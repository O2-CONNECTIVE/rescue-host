package serviceprovision

import (
	"context"
	"slices"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

var _ servicehost.PreviewConfirmationResolver = (*Execution)(nil)

// この原thread上の再検査は確認内容の一致だけを示し、ApprovedPolicyやgrantを作らない。
// Service側のopaque ticket検査と元owner RPC以外の公開入力へは接続しない。
func (s *nativeExecutionStage) VerifyPreviewConfirmation(ctx context.Context, expected servicehost.PreviewDescriptor) error {
	if s == nil || s.preview == nil || s.previewSnapshot == nil || s.prepared == nil || s.token != nil || !rootLive(ctx) {
		return ErrRootDenied
	}
	frozen := *s.previewSnapshot
	if !samePreviewContents(frozen, expected) || !frozen.HardDeadline.Equal(expected.HardDeadline) || !time.Now().Before(frozen.HardDeadline) {
		return ErrRecord
	}
	current, _, _, err := s.prepared.previewRecord(ctx)
	if err != nil {
		return err
	}
	// currentの新しいdeadlineは採用しない。初回構成のhard capを延長しない。
	if s.staging || s.runtimeGuest {
		if s.guest == nil {
			return ErrRootDenied
		}
		current.ConfigDigest, err = s.guest.Digest(ctx)
		if err != nil {
			return err
		}
	}
	if !samePreviewContents(frozen, current) || !rootLive(ctx) || !time.Now().Before(frozen.HardDeadline) {
		return ErrRootDenied
	}
	return nil
}
func samePreviewContents(a, b servicehost.PreviewDescriptor) bool {
	return a.ConfigDigest == b.ConfigDigest && a.PolicyDigest == b.PolicyDigest &&
		a.KeySetDigest == b.KeySetDigest && a.PolicyMode == b.PolicyMode &&
		a.LeaseCeiling == b.LeaseCeiling && slices.Equal(a.AllowedKeyFingerprints, b.AllowedKeyFingerprints)
}

// ResolvePreviewConfirmationは、同Serviceの認証済みconfirmだけが渡すopaque ticketを
// 元Execution ownerへ運ぶ。結果はRecord一致の再検査で、承認/grantの発行ではない。
func (e *Execution) ResolvePreviewConfirmation(ctx context.Context, ticket servicehost.PreviewConfirmationTicket) error {
	if e == nil || !e.configured || e.preview == nil || !rootLive(ctx) {
		return ErrRootDenied
	}
	service := e.service.Load()
	if _, err := ticket.PreviewFor(service); err != nil {
		return ErrRootDenied
	}
	request := executionRequest{ctx: ctx, confirmation: &ticket, reply: make(chan error, 1)}
	select {
	case <-e.cleaned:
		return ErrRootDenied
	case <-ctx.Done():
		return ErrRootDenied
	case e.requests <- request:
	}
	select {
	case <-e.cleaned:
		return ErrRootDenied
	case <-ctx.Done():
		return ErrRootDenied
	case err := <-request.reply:
		return err
	}
}

type previewVerificationStage interface {
	VerifyPreviewConfirmation(context.Context, servicehost.PreviewDescriptor) error
}

func (e *Execution) resolvePreviewOnOwner(ctx context.Context, service *servicehost.Service, ticket servicehost.PreviewConfirmationTicket) error {
	if !rootLive(ctx) || service == nil || service != e.service.Load() || e.preview == nil {
		return ErrRootDenied
	}
	expected, err := ticket.PreviewFor(service)
	if err != nil {
		return ErrRootDenied
	}
	stage, ok := e.stage.(previewVerificationStage)
	if !ok {
		return ErrRootDenied
	}
	if err := callPreviewVerification(ctx, stage, expected); err != nil {
		return err
	}
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	if _, err := ticket.PreviewFor(service); err != nil {
		return ErrRootDenied
	}
	return nil
}

// Source panic後は元scopeの健康を仮定しない。既存fatal退役に資源参照を保持する。
func callPreviewVerification(ctx context.Context, stage previewVerificationStage, expected servicehost.PreviewDescriptor) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRootFatal
		}
	}()
	return stage.VerifyPreviewConfirmation(ctx, expected)
}
