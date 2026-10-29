package serviceprovision

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"testing"
)

func TestNativeFullEntryLoadsFixedGuestBeforeStrongToken(t *testing.T) {
	p, root, _, _ := guestConfigurationFixture(t)
	guestCalls, strongCalls := 0, 0
	stage := &nativeExecutionStage{cfg: ExecutionConfig{Anchor: rootAnchor(), Approval: &noApprovalAtConstruction{}}, calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }, guest: func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) {
		guestCalls++
		return p.loadFixedGuest(ctx, root)
	}, launch: func(context.Context, *Prepared) (*LaunchToken, error) {
		strongCalls++
		if guestCalls != 1 {
			t.Error("guest配置供給より先に強token取得")
		}
		return nil, ErrLaunchDenied
	}}}
	service, e := stage.Initialize(rootContext(t))
	if service != nil || !errors.Is(e, ErrLaunchDenied) || guestCalls != 1 || strongCalls != 1 || stage.guest == nil {
		t.Fatal("実entryから固定guest sourceへ到達しない", e, guestCalls, strongCalls)
	}
	guest := stage.guest
	if e = stage.Close(rootContext(t)); e != nil || !guest.closed.Load() {
		t.Fatal("元thread outer ownerがguest sourceを回収しない", e)
	}
}
func TestNativeFullEntryMissingGuestStopsBeforeStrongOrSCM(t *testing.T) {
	p, root, k, _ := guestConfigurationFixture(t)
	k.files[root.anchor.DigestPath].data[0] = 'f'
	strongCalls := 0
	stage := &nativeExecutionStage{calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }, guest: func(ctx context.Context, p *Prepared) (*GuestConfiguration, error) {
		return p.loadFixedGuest(ctx, root)
	}, launch: func(context.Context, *Prepared) (*LaunchToken, error) { strongCalls++; return nil, ErrLaunchDenied }}}
	var service *servicehost.Service
	service, e := stage.Initialize(rootContext(t))
	if service != nil || e == nil || strongCalls != 0 || stage.guest == nil {
		t.Fatal("guest record拒否後の強取得/構成", e, strongCalls)
	}
	if e = stage.Close(rootContext(t)); e != nil {
		t.Fatal("partial Root owner回収なし", e)
	}
}
