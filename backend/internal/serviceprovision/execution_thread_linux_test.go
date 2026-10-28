package serviceprovision

import "syscall"

func executionTestThreadID() uint64 { return uint64(syscall.Gettid()) }
