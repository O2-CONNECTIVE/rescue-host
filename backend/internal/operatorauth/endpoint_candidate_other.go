//go:build !windows

package operatorauth

func newNativeEndpointCandidateLease(uintptr) (*endpointCandidateLease, error) {
	return nil, ErrUnconfigured
}
