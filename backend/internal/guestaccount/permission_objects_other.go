//go:build !windows

package guestaccount

func newNativePermissionObjectReader() (*permissionObjectReader, error) { return nil, ErrUnconfigured }
