//go:build !windows

package operatorauth

func closeNativeHelperEntryHandle(uintptr) error { return ErrUnconfigured }
