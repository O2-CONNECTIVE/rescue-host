//go:build !windows

package guestaccount

func newNativeAccountQuery(Plan, binding, *seal) (*accountQuery, error) { return nil, ErrUnconfigured }
