//go:build windows

package operatorauth

import "golang.org/x/sys/windows"

func closeNativeHelperEntryHandle(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}
