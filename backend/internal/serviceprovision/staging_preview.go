package serviceprovision

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func (s *nativeExecutionStage) composeStagingPreview(ctx context.Context) (*servicehost.Service, servicehost.PreviewDescriptor, error) {
	preview, r, token, err := s.prepared.previewRecord(ctx)
	if err != nil {
		return nil, servicehost.PreviewDescriptor{}, err
	}
	preview.ConfigDigest, err = s.guest.Digest(ctx)
	if err != nil {
		return nil, servicehost.PreviewDescriptor{}, err
	}
	configuration := servicehost.PreviewServiceConfig{
		Ready:   serviceready.Config{Profile: r.Profile, PrimaryToken: token, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout},
		Budgets: r.Budgets, RequestLimit: s.preview.RequestLimit, LaunchLimit: s.preview.LaunchLimit, IntentTTL: s.preview.IntentTTL, Preview: preview,
	}
	var service *servicehost.Service
	if s.runtimeGuest {
		service, err = servicehost.NewRuntimeService(configuration, s.requestBudgets)
	} else {
		service, err = servicehost.NewPreviewService(configuration)
	}
	return service, preview, err
}
