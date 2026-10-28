package serviceprovision

import "syscall"

// thread ID照会だけを使い、security/token/OS設定は変更しない。
func executionTestThreadID() uint64 {
	id, _, _ := syscall.RawSyscall(syscall.SYS_THREAD_SELFID, 0, 0, 0)
	return uint64(id)
}
