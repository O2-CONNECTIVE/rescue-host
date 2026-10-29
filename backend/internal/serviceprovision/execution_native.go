package serviceprovision

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// nativeExecutionStageは全て元owner threadからだけ呼び出す。
// legacyの強handleとquery-only設定rootは別所有物。previewはquery-onlyだけを
// inner終端後まで保持する。元scopeをSCM threadへ移送しない。
// private seamsはsource/fakeの経路選択検査用。本番は未設定で下記の実関数を使う。
type nativeExecutionCalls struct {
	prepare func(context.Context, Anchor) (*Prepared, error)
	launch  func(context.Context, *Prepared) (*LaunchToken, error)
}
type nativeExecutionStage struct {
	preview  *PreviewLimits
	calls    nativeExecutionCalls
	cfg      ExecutionConfig
	prepared *Prepared
	token    *LaunchToken
}

func (s *nativeExecutionStage) Initialize(ctx context.Context) (service *servicehost.Service, err error) {
	prepare := s.calls.prepare
	if prepare == nil {
		prepare = PrepareFixed
	}
	s.prepared, err = prepare(ctx, s.cfg.Anchor)
	if err != nil {
		return nil, err
	}
	if s.prepared == nil {
		return nil, ErrRootDenied
	}
	if s.preview != nil {
		// query-only rootを借用する。強token/Approval/Launcherは取得しない。
		return s.prepared.ComposePreviewService(ctx, *s.preview)
	}
	launch := s.calls.launch
	if launch == nil {
		launch = prepareExecutionToken
	}
	s.token, err = launch(ctx, s.prepared)
	if err != nil {
		return nil, err
	}
	if s.token == nil {
		return nil, ErrRootDenied
	}
	strong, err := s.token.Borrow(ctx)
	if err != nil {
		return nil, err
	}
	return s.prepared.ComposeService(ctx, strong, s.cfg.Approval)
}

func prepareExecutionToken(ctx context.Context, prepared *Prepared) (*LaunchToken, error) {
	query, err := prepared.loaded.QueryToken()
	if err != nil {
		return nil, err
	}
	r := prepared.record
	factory, err := NewNativeLaunchFactory(serviceready.Config{Profile: r.Profile, PrimaryToken: query, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout})
	if err != nil {
		return nil, err
	}
	return factory.Prepare(ctx)
}

func (s *nativeExecutionStage) Close(ctx context.Context) error {
	if s.token != nil {
		if err := s.token.Close(ctx); err != nil {
			return err
		}
		s.token = nil
	}
	if s.prepared != nil {
		if err := s.prepared.Close(ctx); err != nil {
			return err
		}
		s.prepared = nil
	}
	return nil
}
