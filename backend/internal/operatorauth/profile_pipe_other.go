//go:build !windows

package operatorauth

import "context"

func newNativeProfilePipe(ctx context.Context, producer profilePipeProducer) (*profilePipe, error) {
	if _, _, err := prepareProfilePipe(ctx, producer); err != nil {
		return nil, err
	}
	return nil, ErrUnconfigured
}
