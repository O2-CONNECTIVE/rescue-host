//go:build !windows

package guestaccount

func runtimePlatform(*RuntimeGuest) (runtimeOps, error) { return runtimeOps{}, ErrUnconfigured }
