package guestaccount

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// productionでは既存LaunchTokenを借用する。private seamを承認/取得の証拠にしない。
// 元execution ownerはstagingの全Close ackまでLaunchTokenを閉じない。
type stagingLaunch interface {
	Borrow(context.Context) (uintptr, error)
	Recheck(context.Context) error
}

// 同じService endpoint ownerが発行する検査借用。新listenerやreaderを作らない。
// producerは未接続であり、raw番号から正規leaseを生成してはならない。
type stagingControlLease interface {
	Borrow(context.Context) (uintptr, error)
	Recheck(context.Context) error
	Close(context.Context) error
}
type stagingNativeConfig struct {
	profile  stagingProfile
	prepared *bootstrapauth.PreparedService
	ready    serviceready.Config
	launch   stagingLaunch
	control  stagingControlLease
}

// 固定provisioningからだけ接続する内部候補。Record schemaやUI経路は追加しない。
// 既存Service基盤profileを動的guest SIDへ書き換えて通す経路はない。
func newNativeStaging(ctx context.Context, p Plan, c stagingNativeConfig, session sessionSource, s *seal) (*stagingOwner, error) {
	profile, e := newStagingProfile(p, c.profile)
	keys := c.ready.Profile.Keys
	if e != nil || c.prepared == nil || c.launch == nil || c.control == nil || session == nil || s == nil || !finite(ctx) || c.ready.Profile.ServiceName != profile.serviceName || keys.ProcessUserSID != p.binding.service || keys.OperatorSID != p.binding.operator || keys.ServiceSID != profile.serviceSID || c.ready.PrimaryToken != profile.native.token {
		return nil, ErrUnconfigured
	}
	factory, e := serviceready.NewNative(c.ready)
	if e != nil {
		return nil, ErrUnconfigured
	}
	ops, e := stagingPlatform(p, c, factory)
	if e != nil {
		return nil, e
	}
	return newStaging(ctx, p, profile, session, s, ops)
}
