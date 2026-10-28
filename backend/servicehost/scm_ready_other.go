//go:build !windows

package servicehost

// RunReadySCMは他OSでservice/consoleへfallbackしない。
func RunReadySCM(*Service) error { return ErrUnsupportedPlatform }
