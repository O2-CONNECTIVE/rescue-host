//go:build !windows

package servicehost

// RunSCM は Windows 以外で service/console/child を起動しない。
func RunSCM() error { return ErrUnsupportedPlatform }
