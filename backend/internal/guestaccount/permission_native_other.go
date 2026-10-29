//go:build !windows

package guestaccount

func newNativePermissionInspector(Plan, binding, *seal, permissionProfile) (*creationQueryOwner, error) {
	return nil, ErrUnconfigured
}
