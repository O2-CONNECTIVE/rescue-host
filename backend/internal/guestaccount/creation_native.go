package guestaccount

// 正規provisioning供給は未実装。公開NewNativeを開かず、typed内部構成だけを準備する。
func newNativeCreationSource(p Plan, s *seal, profile sidNativeProfile) (*creationSource, error) {
	if !p.valid || s == nil || profile.stdio.UserSID != p.binding.service {
		return nil, ErrUnconfigured
	}
	create, err := newGuestNativeFactory(profile, creationScript(profile.manifest.Path))
	if err != nil {
		return nil, err
	}
	cleanup, err := newSIDNativeFactory(profile)
	if err != nil {
		return nil, err
	}
	preflight, err := newNativeCreationCheck(p, s)
	if err != nil {
		return nil, err
	}
	return newCreationSource(p, s, creationDependencies{preflight: preflight, create: create, cleanup: cleanup, inspect: func(b binding) (ownedAccountInspector, error) {
		return newNativeCreationInspector(p, b, s)
	}})
}
