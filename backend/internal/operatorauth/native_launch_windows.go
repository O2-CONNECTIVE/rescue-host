//go:build windows

package operatorauth

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeHelperKernel struct{}

// This private adapter is source preparation only. Ancestor ACL, report-client
// provenance and service protection must come from the still missing factory.
func startNativeHelper(ctx context.Context, s nativeLaunchSpec) (*helperChild, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	token, err := nativeOpenThreadToken()
	if token != 0 {
		_ = token.Close()
		return nil, ErrDenied
	}
	if err != windows.ERROR_NO_TOKEN {
		return nil, ErrDenied
	}
	return startHelper(ctx, s, nativeHelperKernel{})
}

var launchGetHandleInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
var launchQueryObject = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")

// ObjectBasicInformation has a fixed 56-byte PUBLIC_OBJECT_BASIC_INFORMATION
// header; only Attributes and GrantedAccess are used. Unknown results reject.
func launchHandleAccess(h uintptr, inherit bool, exact uint32) error {
	if !helperHandle(h) || launchGetHandleInfo.Find() != nil || launchQueryObject.Find() != nil {
		return ErrDenied
	}
	var flags uint32
	ok, _, _ := launchGetHandleInfo.Call(h, uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || (flags&windows.HANDLE_FLAG_INHERIT != 0) != inherit || flags & ^uint32(windows.HANDLE_FLAG_INHERIT) != 0 {
		return ErrDenied
	}
	var basic [14]uint32
	var size uint32
	status, _, _ := launchQueryObject.Call(h, 0, uintptr(unsafe.Pointer(&basic[0])), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&size)))
	if status != 0 || size != uint32(unsafe.Sizeof(basic)) || basic[1] != exact {
		return ErrDenied
	}
	return nil
}
func (nativeHelperKernel) validate(ctx context.Context, s nativeLaunchSpec) error {
	if !finite(ctx) || !s.helperIdentityValid() {
		return ErrDenied
	}
	// The token is borrowed; no logon, privilege adjustment or fallback occurs.
	if launchHandleAccess(s.primaryToken, false, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY) != nil {
		return ErrDenied
	}
	p, err := launchPrimaryProjection(windows.Token(s.primaryToken))
	if err != nil || p.user != s.helperUserSID {
		return ErrDenied
	}
	if s.helperServiceSID != "" {
		if _, err = launchServiceGroup(windows.Token(s.primaryToken), s.helperServiceSID); err != nil {
			return ErrDenied
		}
	}
	var own windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &own) != nil {
		return ErrDenied
	}
	session, sessionErr := nativeTokenDWORD(own, windows.TokenSessionId)
	closeErr := own.Close()
	if sessionErr != nil || closeErr != nil || session != p.session {
		return ErrDenied
	}
	for _, pipe := range []struct {
		h      uintptr
		access uint32
	}{{s.operatorRead, 0x120089}, {s.reportWrite, 0x120102}, {s.profileRead, 0x120089}} {
		kind, e := windows.GetFileType(windows.Handle(pipe.h))
		if e != nil || kind != windows.FILE_TYPE_PIPE || launchHandleAccess(pipe.h, true, pipe.access) != nil {
			return ErrDenied
		}
	}
	var flags uint32
	var mode uint32
	if windows.GetNamedPipeInfo(windows.Handle(s.operatorRead), &flags, nil, nil, nil) != nil || flags != 5 || windows.GetNamedPipeHandleState(windows.Handle(s.operatorRead), &mode, nil, nil, nil, nil, 0) != nil || mode != windows.PIPE_READMODE_MESSAGE {
		return ErrDenied
	}
	// GetNamedPipeInfo is deliberately not called on the write-only client: its
	// access requirements would broaden rights. Exact rights do not prove client
	// provenance; only the unimplemented private report-pair factory can provide it.
	return nil
}
func (nativeHelperKernel) newJob() (uintptr, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return uintptr(h), ErrDenied
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// This bounds lifetime/process count, not OS privilege isolation.
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	limits.BasicLimitInformation.ActiveProcessLimit = 1
	_, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	return uintptr(h), err
}
func launchSecurity(sddl string) (*windows.SecurityAttributes, error) {
	if !strings.HasPrefix(sddl, "D:P") {
		return nil, ErrDenied
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, ErrDenied
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil || dacl == nil || defaulted {
		return nil, ErrDenied
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}
func (nativeHelperKernel) createSuspended(s nativeLaunchSpec) (uintptr, uintptr, error) {
	processSA, err := launchSecurity(s.processSDDL)
	if err != nil {
		return 0, 0, err
	}
	threadSA, err := launchSecurity(s.threadSDDL)
	if err != nil {
		return 0, 0, err
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, 0, err
	}
	defer attributes.Delete()
	handles := [3]windows.Handle{windows.Handle(s.operatorRead), windows.Handle(s.reportWrite), windows.Handle(s.profileRead)}
	if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return 0, 0, err
	}
	image, err := windows.UTF16PtrFromString(s.imagePath)
	if err != nil {
		return 0, 0, err
	}
	command, err := windows.UTF16FromString(`"` + s.imagePath + `" --operator-auth-internal ` + strconv.FormatUint(uint64(s.operatorRead), 10) + " " + strconv.FormatUint(uint64(s.reportWrite), 10) + " " + strconv.FormatUint(uint64(s.profileRead), 10) + " " + s.launchID)
	if err != nil {
		return 0, 0, err
	}
	lastSeparator := strings.LastIndex(s.imagePath, `\`)
	if lastSeparator == 2 {
		lastSeparator++
	}
	directory, err := windows.UTF16PtrFromString(s.imagePath[:lastSeparator])
	if err != nil {
		return 0, 0, err
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.ProcThreadAttributeList = attributes.List()
	environment := [2]uint16{}
	var process windows.ProcessInformation
	// No STARTF_USESTDHANDLES; only the three explicit handles are inherited.
	err = windows.CreateProcessAsUser(windows.Token(s.primaryToken), image, &command[0], processSA, threadSA, true, windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, &environment[0], directory, &startup.StartupInfo, &process)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(processSA)
	runtime.KeepAlive(threadSA)
	runtime.KeepAlive(environment)
	return uintptr(process.Process), uintptr(process.Thread), err
}
func (nativeHelperKernel) assign(job, process uintptr) error {
	return windows.AssignProcessToJobObject(windows.Handle(job), windows.Handle(process))
}
func (nativeHelperKernel) resume(thread uintptr) error {
	previous, err := windows.ResumeThread(windows.Handle(thread))
	if err != nil || previous != 1 {
		return ErrUnconfirmed
	}
	return nil
}
func (nativeHelperKernel) terminateJob(job uintptr) error {
	return windows.TerminateJobObject(windows.Handle(job), 1)
}
func (nativeHelperKernel) terminateProcess(process uintptr) error {
	return windows.TerminateProcess(windows.Handle(process), 1)
}
func (nativeHelperKernel) close(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}
func (nativeHelperKernel) waitProcess(ctx context.Context, process uintptr) (uint32, error) {
	return (&nativeProcess{handle: process}).wait(ctx)
}
