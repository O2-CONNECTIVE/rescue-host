//go:build !windows

package guestaccount

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"

func stagingPlatform(Plan, stagingNativeConfig, *serviceready.Factory) (stagingOps, error) {
	return stagingOps{}, ErrUnconfigured
}
