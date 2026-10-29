package serviceprovision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func confirmationStageFixture(t *testing.T) (*nativeExecutionStage, *Prepared, *subjectFaultKernel, *previewMemoryScope) {
	t.Helper()
	p, kernel, scope := previewPreparedFixture(t)
	limits := previewExecutionConfig().Limits
	stage := &nativeExecutionStage{cfg: ExecutionConfig{Anchor: rootAnchor()}, preview: &limits,
		calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }}}
	service, err := stage.Initialize(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(rootContext(t)); err != nil {
			t.Error(err)
		}
		if err := stage.Close(rootContext(t)); err != nil && !errors.Is(err, ErrRootFatal) {
			t.Error(err)
		}
	})
	return stage, p, kernel, scope
}

func confirmationExpected(stage *nativeExecutionStage) servicehost.PreviewDescriptor {
	expected := *stage.previewSnapshot
	expected.AllowedKeyFingerprints = append([]string(nil), expected.AllowedKeyFingerprints...)
	return expected
}

func TestConfirmationRecordRecheckKeepsOriginalDeadlineAndNeverCreatesGrant(t *testing.T) {
	stage, _, _, scope := confirmationStageFixture(t)
	expected := confirmationExpected(stage)
	before := scope.rechecks
	if err := stage.VerifyPreviewConfirmation(rootContext(t), expected); err != nil {
		t.Fatal(err)
	}
	if scope.rechecks != before+1 || !stage.previewSnapshot.HardDeadline.Equal(expected.HardDeadline) || stage.token != nil {
		t.Fatal("confirmation did not recheck original scope or replaced deadline/token")
	}
}

func TestConfirmationRecordRejectsCandidateDriftAndUnavailableRoot(t *testing.T) {
	for _, kind := range []string{"config", "policy", "keyset", "fingerprint", "mode", "lease", "deadline", "expired", "scope", "closed", "fatal", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			stage, _, kernel, scope := confirmationStageFixture(t)
			expected := confirmationExpected(stage)
			ctx := rootContext(t)
			switch kind {
			case "config":
				expected.ConfigDigest[0] ^= 1
			case "policy":
				expected.PolicyDigest[0] ^= 1
			case "keyset":
				expected.KeySetDigest[0] ^= 1
			case "fingerprint":
				expected.AllowedKeyFingerprints[0] = "changed"
			case "mode":
				expected.PolicyMode = "full"
			case "lease":
				expected.LeaseCeiling++
			case "deadline":
				expected.HardDeadline = expected.HardDeadline.Add(time.Nanosecond)
			case "expired":
				stage.previewSnapshot.HardDeadline = time.Now().Add(-time.Second)
				expected = confirmationExpected(stage)
			case "scope":
				scope.err = ErrRootDenied
			case "closed":
				if err := stage.Close(ctx); err != nil {
					t.Fatal(err)
				}
			case "fatal":
				kernel.thread++
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := stage.VerifyPreviewConfirmation(ctx, expected); err == nil {
				t.Fatal("changed or unavailable root accepted")
			}
			if stage.token != nil {
				t.Fatal("confirmation acquired a strong token")
			}
		})
	}
}

func TestConfirmationRPCRejectsUnboundTicketWithoutCallingOwner(t *testing.T) {
	e, err := NewNativePreviewExecution(previewExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.ResolvePreviewConfirmation(ctx, servicehost.PreviewConfirmationTicket{}); !errors.Is(err, ErrRootDenied) {
		t.Fatal("unbound confirmation entered owner RPC", err)
	}
	if err := (*Execution)(nil).ResolvePreviewConfirmation(ctx, servicehost.PreviewConfirmationTicket{}); !errors.Is(err, ErrRootDenied) {
		t.Fatal(err)
	}
}

type panicPreviewVerification struct{}

func (panicPreviewVerification) VerifyPreviewConfirmation(context.Context, servicehost.PreviewDescriptor) error {
	panic("synthetic source panic")
}

func TestConfirmationSourcePanicKeepsFatalClassification(t *testing.T) {
	if err := callPreviewVerification(rootContext(t), panicPreviewVerification{}, servicehost.PreviewDescriptor{}); !errors.Is(err, ErrRootFatal) {
		t.Fatal("owner source panic became a normal retry", err)
	}
}
