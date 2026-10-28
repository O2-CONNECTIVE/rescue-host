//go:build !windows

package serviceprovision

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"

func launchPlatform(*serviceready.Factory) launchKernel { return nil }
