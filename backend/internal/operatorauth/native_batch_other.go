//go:build !windows

package operatorauth

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

func startNativeBatch(context.Context, nativeBatchSpec, *bootstrapauth.ServiceScope, batchOwner) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}
