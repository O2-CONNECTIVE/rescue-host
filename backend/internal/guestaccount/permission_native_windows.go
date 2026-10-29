//go:build windows

package guestaccount

// constructorはAPIを実行しない。全readは既存query ownerの固定thread内だけ。
func newNativePermissionInspector(p Plan, b binding, s *seal, profile permissionProfile) (*creationQueryOwner, error) {
	q, e := newNativeAccountQuery(p, b, s)
	if e != nil {
		return nil, e
	}
	facts, e := newNativePermissionFactsCollector(q)
	if e != nil {
		return nil, e
	}
	groups, e := newNativePermissionGroupsCollector(q)
	if e != nil {
		return nil, e
	}
	objects, e := newNativePermissionObjectReader()
	if e != nil {
		return nil, e
	}
	if e = configurePermissionInspection(q, profile, facts.kernel, groups.kernel, objects); e != nil {
		return nil, e
	}
	return newCreationQueryOwner(q, false)
}
