package serviceprovision

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// nativeExecutionStageは全て元owner threadからだけ呼び出す。
// 子へ渡す強handleとquery-only設定rootは別所有物で、inner終端後まで双方を保持する。
type nativeExecutionStage struct {
	cfg      ExecutionConfig
	prepared *Prepared
	token    *LaunchToken
}

func (s *nativeExecutionStage) Initialize(ctx context.Context) (service *servicehost.Service, err error) {
	s.prepared, err = PrepareFixed(ctx, s.cfg.Anchor)
	if err != nil {
		return nil, err
	}
	query, err := s.prepared.loaded.QueryToken()
	if err != nil {
		return nil, err
	}
	r := s.prepared.record
	factory, err := NewNativeLaunchFactory(serviceready.Config{Profile: r.Profile, PrimaryToken: query, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout})
	if err != nil {
		return nil, err
	}
	s.token, err = factory.Prepare(ctx)
	if err != nil {
		return nil, err
	}
	strong, err := s.token.Borrow(ctx)
	if err != nil {
		return nil, err
	}
	return s.prepared.ComposeService(ctx, strong, s.cfg.Approval)
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
