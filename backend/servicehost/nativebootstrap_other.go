//go:build !windows

package servicehost

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

func closeNativeChildPipes(context.Context, *childjob.Handles, uintptr) error {
	return ErrUnsupportedPlatform
}
