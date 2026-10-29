package guestaccount

import (
	"context"
	"errors"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

func TestStagingRejectsMissingTrustInputsBeforeAnyAcquisition(t *testing.T) {
	for _, name := range []string{"session", "session-seal", "profile", "ops", "cancel"} {
		t.Run(name, func(t *testing.T) {
			p, c, f, k, ops := stagingTest(t)
			var session sessionSource = f
			s := f.s.sessionSeal
			ctx := testContext(t)
			switch name {
			case "session":
				session = nil
			case "session-seal":
				s = nil
			case "profile":
				c.installation = [32]byte{}
			case "ops":
				ops.accounts = nil
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if o, e := newStaging(ctx, p, c, session, s, ops); e == nil || o != nil || k.principalCalls != 0 || len(k.refs) != 0 || k.accounts != 0 {
				t.Fatal("未設定の信頼入力から取得/副作用", o, e)
			}
		})
	}
}
func TestStagingPartialAcquisitionRetainsOwnerAndHealthyCleanup(t *testing.T) {
	for _, name := range []string{"partial-open", "identity", "cancel", "scope-close"} {
		t.Run(name, func(t *testing.T) {
			p, c, f, k, ops := stagingTest(t)
			ctx := testContext(t)
			switch name {
			case "partial-open":
				open := ops.open
				ops.open = func(ctx context.Context, kind permissionObjectKind, file stagingFile, g func() bool) (stagingResource, error) {
					r, _ := open(ctx, kind, file, g)
					return r, ErrDenied
				}
			case "identity":
				inspect := ops.inspect
				ops.inspect = func(r stagingResource, g func() bool) (journalFileInfo, error) {
					i, e := inspect(r, g)
					i.identity.fileID[0]++
					return i, e
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				k.openHook = func(permissionObjectKind) { cancel() }
			case "scope-close":
				k.scope.err = ErrDenied
				k.scope.closeErr = ErrCleanup
			}
			o, e := newStaging(ctx, p, c, f, f.s.sessionSeal, ops)
			if e == nil || o == nil || k.accounts != 0 {
				t.Fatal("部分取得を成功/参照破棄", o, e)
			}
			e = o.Close(testContext(t))
			if name == "scope-close" {
				if e == nil || o.scope == nil {
					t.Fatal("未確認scopeを破棄")
				}
				k.scope.closeErr = nil
				e = o.Close(testContext(t))
			}
			if e != nil {
				t.Fatal("健康な元scopeで回収できない", e)
			}
			for _, r := range k.refs {
				if r.closes != 1 {
					t.Fatal("部分handleの回収なし")
				}
			}
		})
	}
}
func TestStagingFatalAcquisitionNeverRestartsNativeOrCleanup(t *testing.T) {
	for _, name := range []string{"dirty", "panic"} {
		t.Run(name, func(t *testing.T) {
			p, c, f, k, ops := stagingTest(t)
			k.openHook = func(permissionObjectKind) {
				if name == "panic" {
					panic("synthetic unknown return")
				}
				k.principal.modifiedID++
			}
			o, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
			if !errors.Is(e, bootstrapauth.ErrFatal) || o == nil || !o.fatal || k.accounts != 0 {
				t.Fatal("未知主体を通常拒否へ変換", o, e)
			}
			calls := k.principalCalls
			if e = o.Close(testContext(t)); !errors.Is(e, bootstrapauth.ErrFatal) || k.principalCalls != calls || k.scope.closes != 0 {
				t.Fatal("fatal後にnative/Close再開", e)
			}
			for _, r := range k.refs {
				if r.closes != 0 {
					t.Fatal("unknown返却の参照を解放")
				}
			}
		})
	}
}
