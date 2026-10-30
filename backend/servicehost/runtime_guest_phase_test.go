package servicehost

import (
	"context"
	"slices"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// The existing private target/owner seams model operation ordering only. They
// supply no account, token, DACL, or native runtime evidence.
type runtimePreparingTargetFixture struct {
	confirmationTargetMemory
	prepare func(context.Context, PreviewConfirmationTicket) error
}

func (m *runtimePreparingTargetFixture) PrepareTarget(ctx context.Context, ticket PreviewConfirmationTicket) error {
	return m.prepare(ctx, ticket)
}

func TestRuntimeTargetPreparationPrecedesInspectionAndAcquire(t *testing.T) {
	s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return nil })
	var order []string
	model := &runtimePreparingTargetFixture{}
	model.prepare = func(ctx context.Context, ticket PreviewConfirmationTicket) error {
		if _, err := ticket.PreviewFor(s); err != nil {
			return err
		}
		order = append(order, "prepare")
		return nil
	}
	model.onInspect = func() { order = append(order, "inspect") }
	source := grantRuntimeFixture(t, s)
	source.target = &confirmationTarget{ops: model}
	source.owner.target = source.target
	original := source.kernel
	source.kernel = runtimeKernelModel(func(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
		order = append(order, "acquire")
		return original.Acquire(ctx, ticket)
	})
	if err := s.registerConfirmationTarget(source); err != nil {
		t.Fatal(err)
	}
	hello := previewExchange(t, a, dispatchHello)
	intent := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	approved := previewExchange(t, a, confirmationFrame(hello, intent.Intent, 2, "33333333333333333333333333333333"))
	if approved.Approval == nil || !slices.Equal(order, []string{"prepare", "inspect", "acquire"}) {
		t.Fatal("runtime phase order changed or skipped preparation", order, approved)
	}
}
