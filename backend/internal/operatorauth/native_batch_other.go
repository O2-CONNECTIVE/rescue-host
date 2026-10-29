//go:build !windows

package operatorauth

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

func startNativeBatch(context.Context, nativeBatchSpec, *bootstrapauth.Factory, uintptr, batchOwner) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}

// No protected native source is configured on this platform.
func prepareNativeBatch(nativeBatchSpec, *bootstrapauth.Factory, uintptr, batchOwner) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}
