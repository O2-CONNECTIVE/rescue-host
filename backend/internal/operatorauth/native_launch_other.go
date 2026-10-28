//go:build !windows

package operatorauth

import "context"

func startNativeHelper(context.Context, nativeLaunchSpec) (*helperChild, error) {
	return nil, ErrUnconfigured
}
