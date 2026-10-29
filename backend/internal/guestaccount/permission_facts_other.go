//go:build !windows

package guestaccount

func newNativePermissionFactsCollector(*accountQuery) (*permissionFactsCollector, error) {
	return nil, ErrUnconfigured
}
