//go:build !windows

package bootstrap

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapio"
)

func closeAuthenticatedReadHandle(context.Context, uintptr) error { return bootstrapio.ErrUnavailable }
