//go:build !windows

package guestaccount

func newNativePermissionGroupsCollector(*accountQuery) (*permissionGroupsCollector, error) {
	return nil, ErrUnconfigured
}
