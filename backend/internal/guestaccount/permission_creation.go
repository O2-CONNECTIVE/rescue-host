package guestaccount

// 固定sourceからのみ構成する候補。公開NewNativeやUI値に接続しない。
func newNativeLimitedCreationSource(p Plan, s *seal, native sidNativeProfile, profile permissionProfile) (*creationSource, error) {
	logonSID := "S-1-5-2"
	if p.policy.batch {
		logonSID = "S-1-5-3"
	}
	if !p.valid || !permissionObjectsConfigured(p.binding, profile.serviceSID, profile.objects) || !containsPermissionSID(p.policy.groups, logonSID) {
		return nil, ErrUnconfigured
	}
	c, e := newNativeCreationSource(p, s, native)
	if e != nil {
		return nil, e
	}
	preflight, ok := c.deps.preflight.(*creationQueryOwner)
	if !ok || !preflight.valid() {
		return nil, ErrUnconfigured
	}
	preflight.query.standalone = true
	profile.objects = append([]permissionObject(nil), profile.objects...)
	c.deps.inspect = func(b binding) (ownedAccountInspector, error) { return newNativePermissionInspector(p, b, s, profile) }
	return c, nil
}
