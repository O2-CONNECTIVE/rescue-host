//go:build !windows

package operatorauth

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"

func prepareNativeEndpointProfileBatch(nativeBatchSpec, *bootstrapauth.Factory, uintptr, batchOwner, profileProviderPolicy) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}
