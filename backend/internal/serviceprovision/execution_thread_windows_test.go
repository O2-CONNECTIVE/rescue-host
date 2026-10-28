package serviceprovision

import "golang.org/x/sys/windows"

func executionTestThreadID() uint64 { return uint64(windows.GetCurrentThreadId()) }
