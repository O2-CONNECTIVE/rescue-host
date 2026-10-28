//go:build !windows

package bootstrapio

func nativeKernel() (kernel, error) { return nil, ErrUnavailable }
