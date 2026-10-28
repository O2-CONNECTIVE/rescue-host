//go:build !windows

package childstdio

func nativeKernel() (kernel, error) { return nil, ErrUnavailable }
