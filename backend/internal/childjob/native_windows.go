//go:build windows

package childjob

import (
	"context"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsKernel struct{}

func platformStart(ctx context.Context, spec Spec) (*Child, error) {
	// Jobの既定descriptor等をimpersonated guestのcontextで作らない。
	// pseudo tokenは取得handleを増やさない。未対応/読取不能も拒否する。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := tokenType(windows.GetCurrentThreadToken()); err != windows.ERROR_NO_TOKEN {
		return nil, ErrConfig
	}
	if kind, err := tokenType(windows.Token(spec.PrimaryToken)); err != nil || kind != windows.TokenPrimary {
		return nil, ErrConfig
	}
	return start(ctx, spec, windowsKernel{})
}

func (windowsKernel) newJob() (uintptr, error) {
	return createOwnedJob(nil)
}

func createOwnedJob(attributes *windows.SecurityAttributes) (uintptr, error) {
	h, err := windows.CreateJobObject(attributes, nil)
	runtime.KeepAlive(attributes)
	if err != nil {
		return 0, ErrStart
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	// breakaway、inherit、名前によるopenは提供しない。設定失敗でもhandleを回収側へ返す。
	return uintptr(h), err
}

var getHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")

func inheritablePipe(h windows.Handle) bool {
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_PIPE {
		return false
	}
	var flags uint32
	ok, _, _ := getHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	return ok != 0 && flags&windows.HANDLE_FLAG_INHERIT != 0
}

func (windowsKernel) createSuspended(spec Spec) (uintptr, uintptr, error) {
	plan, err := makePlan(spec)
	if err != nil {
		return 0, 0, err
	}
	values := plan.inheritedHandles()
	handles := make([]windows.Handle, len(values))
	for i, value := range values {
		handles[i] = windows.Handle(value)
	}
	for _, handle := range handles {
		if !inheritablePipe(handle) {
			return 0, 0, ErrConfig
		}
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, 0, ErrStart
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return 0, 0, ErrStart
	}
	image, err := windows.UTF16PtrFromString(plan.application)
	if err != nil {
		return 0, 0, ErrConfig
	}
	command, _ := windows.UTF16FromString(plan.commandLine)
	directory, _ := windows.UTF16PtrFromString(plan.directory)
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = plan.startFlags
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[2]
	startup.ProcThreadAttributeList = attributes.List()
	process := windows.ProcessInformation{}
	processAttributes, err := protectedAttributes(spec.Protection, protectedProcess)
	if err != nil {
		return 0, 0, err
	}
	threadAttributes, err := protectedAttributes(spec.Protection, protectedThread)
	if err != nil {
		return 0, 0, err
	}
	// 親の秘密環境、cwd、検索pathを継承せず、明示tokenでのみ起動する。
	// CreateProcess/CreateProcessWithLogonやLocalSystemへのfallbackはない。
	err = windows.CreateProcessAsUser(windows.Token(spec.PrimaryToken), image, &command[0], processAttributes, threadAttributes, true, plan.flags, &plan.environment[0], directory, &startup.StartupInfo, &process)
	runtime.KeepAlive(processAttributes)
	runtime.KeepAlive(threadAttributes)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(plan)
	return uintptr(process.Process), uintptr(process.Thread), err
}

func (windowsKernel) assign(job, process uintptr) error {
	return windows.AssignProcessToJobObject(windows.Handle(job), windows.Handle(process))
}
func (windowsKernel) resume(thread uintptr) error {
	previous, err := windows.ResumeThread(windows.Handle(thread))
	if err != nil || previous != 1 {
		return ErrStart
	}
	return nil
}
func (windowsKernel) terminateJob(job uintptr) error {
	return windows.TerminateJobObject(windows.Handle(job), 1)
}
func (windowsKernel) terminateProcess(process uintptr) error {
	return windows.TerminateProcess(windows.Handle(process), 1)
}
func (windowsKernel) close(handle uintptr) error { return windows.CloseHandle(windows.Handle(handle)) }

func (windowsKernel) waitProcess(ctx context.Context, process uintptr) (uint32, error) {
	for ctx.Err() == nil {
		status, err := windows.WaitForSingleObject(windows.Handle(process), 10)
		if err != nil {
			return 0, ErrUnconfirmed
		}
		if status == windows.WAIT_OBJECT_0 {
			var exit uint32
			if err := windows.GetExitCodeProcess(windows.Handle(process), &exit); err != nil || ctx.Err() != nil {
				return 0, ErrUnconfirmed
			}
			return exit, nil
		}
		if status != uint32(windows.WAIT_TIMEOUT) {
			return 0, ErrUnconfirmed
		}
	}
	return 0, ErrUnconfirmed
}

type jobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
	PageFaultCount, TotalProcesses, ActiveProcesses, Terminated      uint32
}

func (windowsKernel) waitEmpty(ctx context.Context, job uintptr) error {
	for ctx.Err() == nil {
		info := jobAccounting{}
		err := windows.QueryInformationJobObject(windows.Handle(job), windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
		if err != nil {
			return ErrUnconfirmed
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ErrUnconfirmed
}
