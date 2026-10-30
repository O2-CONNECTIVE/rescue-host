package serviceprovision

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func TestRuntimeExecutionRequiresExplicitRequestBudgetsBeforeAcquisition(t *testing.T) {
	if execution, err := NewNativeRuntimeExecution(previewExecutionConfig()); err == nil || execution != nil {
		t.Fatal("runtime accepted preview-only configuration without finite request budgets")
	}
}

func TestRuntimeNativeCompositionCannotFallBackToPreviewWithoutRequestBudgets(t *testing.T) {
	p, root, _, _ := runtimeConfigurationFixture(t)
	limits := previewExecutionConfig().Limits
	stage := &nativeExecutionStage{preview: &limits, runtimeGuest: true,
		calls: nativeExecutionCalls{
			prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil },
			guest: func(ctx context.Context, got *Prepared) (*GuestConfiguration, error) {
				return got.loadRuntimeGuestConfiguration(ctx, root)
			},
		},
	}
	service, err := stage.Initialize(rootContext(t))
	if service != nil {
		if closeErr := service.Close(rootContext(t)); closeErr != nil {
			t.Error(closeErr)
		}
	}
	if err == nil || service != nil {
		t.Error("runtime stage silently selected preview without explicit reception budgets")
	}
	if err := stage.Close(rootContext(t)); err != nil {
		t.Fatal("rejected composition lost protected source ownership", err)
	}
}

func runtimeRequestBudgets() servicehost.RequestLoopBudgets {
	return servicehost.RequestLoopBudgets{Wait: time.Second, Request: 2 * time.Second}
}

func runtimeExecutionConfig() PreviewExecutionConfig {
	c := previewExecutionConfig()
	c.RuntimeRequests = runtimeRequestBudgets()
	return c
}
