//go:build !windows

package operatorauth

func nativeProfileReceiverOps() (profileReceiverOps, error) {
	return profileReceiverOps{}, ErrUnconfigured
}
