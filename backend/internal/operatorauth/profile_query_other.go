//go:build !windows

package operatorauth

func newNativeProfileQuery(uintptr, uintptr) (*profileQuery, error) { return nil, ErrUnconfigured }
