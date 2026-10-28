//go:build windows

package bootstrapauth

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 実 OS では未実行です。query-only API は OS の非 return を解決しません。
type windowsKernel struct{}

func platformKernel() kernel           { return windowsKernel{} }
func (windowsKernel) threadID() uint32 { return windows.GetCurrentThreadId() }
func (windowsKernel) cleanThread() error {
	var kind, returned uint32
	err := windows.GetTokenInformation(windows.GetCurrentThreadToken(), windows.TokenType, (*byte)(unsafe.Pointer(&kind)), 4, &returned)
	if err != windows.ERROR_NO_TOKEN {
		return ErrFatal
	}
	return nil
}
func (windowsKernel) principal(token uintptr) (identity, error) {
	t := windows.GetCurrentProcessToken()
	if token != 0 {
		t = windows.Token(token)
	}
	return tokenIdentity(t)
}
func tokenIdentity(token windows.Token) (identity, error) {
	var kind, returned uint32
	if windows.GetTokenInformation(token, windows.TokenType, (*byte)(unsafe.Pointer(&kind)), 4, &returned) != nil || returned != 4 {
		return identity{}, ErrDenied
	}
	u, e := token.GetTokenUser()
	if e != nil || u == nil || u.User.Sid == nil || !u.User.Sid.IsValid() {
		return identity{}, ErrDenied
	}
	groups, e := token.GetTokenGroups()
	if e != nil || groups == nil || groups.GroupCount > 1024 {
		return identity{}, ErrDenied
	}
	out := identity{user: u.User.Sid.String(), kind: kind}
	for _, g := range groups.AllGroups() {
		if g.Sid == nil || !g.Sid.IsValid() {
			return identity{}, ErrDenied
		}
		out.groups = append(out.groups, group{sid: g.Sid.String(), attributes: g.Attributes})
	}
	runtime.KeepAlive(u)
	runtime.KeepAlive(groups)
	return out, nil
}
func (windowsKernel) currentPID() uint32            { return windows.GetCurrentProcessId() }
func (windowsKernel) currentImage() (string, error) { return processPath(windows.CurrentProcess()) }
func processPath(h windows.Handle) (string, error) {
	var buf [2048]uint16
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil || n == 0 || n >= uint32(len(buf)) {
		return "", ErrDenied
	}
	return windows.UTF16ToString(buf[:n]), nil
}
func (windowsKernel) inspectPipe(handle uintptr) (pipeState, error) {
	if !realHandle(handle) {
		return pipeState{}, ErrDenied
	}
	h := windows.Handle(handle)
	kind, e := windows.GetFileType(h)
	if e != nil || kind != windows.FILE_TYPE_PIPE {
		return pipeState{}, ErrDenied
	}
	// PUBLIC_OBJECT_BASIC_INFORMATION の GrantedAccess だけを照会します。
	// API 不在・unknown status は別権限による代替照会を行わず拒否します。
	var basic struct {
		Attributes, GrantedAccess, HandleCount, PointerCount uint32
		Reserved                                             [10]uint32
	}
	var returned uint32
	query := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")
	if query.Find() != nil {
		return pipeState{}, ErrDenied
	}
	status, _, _ := query.Call(handle, 0, uintptr(unsafe.Pointer(&basic)), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&returned)))
	if status != 0 || returned != uint32(unsafe.Sizeof(basic)) {
		return pipeState{}, ErrDenied
	}
	var flags, mode, pid uint32
	if windows.GetNamedPipeInfo(h, &flags, nil, nil, nil) != nil || windows.GetNamedPipeHandleState(h, &mode, nil, nil, nil, nil, 0) != nil || windows.GetNamedPipeClientProcessId(h, &pid) != nil {
		return pipeState{}, ErrDenied
	}
	owner, acl, protected, e := security(h)
	if e != nil {
		return pipeState{}, e
	}
	return pipeState{kind: kind, flags: flags, mode: mode, access: basic.GrantedAccess, owner: owner, protected: protected, acl: acl, pid: pid}, nil
}

type nativeService struct {
	manager, service windows.Handle
	thread           uint32
}

func (windowsKernel) openService(name string) (serviceHandle, error) {
	ptr, e := windows.UTF16PtrFromString(name)
	if e != nil {
		return nil, ErrDenied
	}
	mgr, e := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if e != nil || mgr == 0 {
		return nil, ErrDenied
	}
	out := &nativeService{manager: mgr, thread: windows.GetCurrentThreadId()}
	svc, e := windows.OpenService(mgr, ptr, windows.SERVICE_QUERY_STATUS)
	if e != nil || svc == 0 {
		return out, ErrDenied
	}
	out.service = svc
	return out, nil
}
func (s *nativeService) status() (serviceState, error) {
	if windows.GetCurrentThreadId() != s.thread {
		return serviceState{}, ErrFatal
	}
	var value windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if s.service == 0 || windows.QueryServiceStatusEx(s.service, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &needed) != nil {
		return serviceState{}, ErrDenied
	}
	return serviceState{pid: value.ProcessId, state: value.CurrentState}, nil
}

// close は取得済み handle の解放専用で、thread の再認証を行いません。
func (s *nativeService) close() error {
	if s.service != 0 {
		if windows.CloseServiceHandle(s.service) == nil {
			s.service = 0
		}
	}
	if s.manager != 0 {
		if windows.CloseServiceHandle(s.manager) == nil {
			s.manager = 0
		}
	}
	if s.service != 0 || s.manager != 0 {
		return ErrUnconfirmed
	}
	return nil
}

type nativeProcess struct {
	process windows.Handle
	token   windows.Token
	thread  uint32
}

func (windowsKernel) openProcess(pid uint32) (processHandle, error) {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if e != nil || h == 0 {
		return nil, ErrDenied
	}
	out := &nativeProcess{process: h, thread: windows.GetCurrentThreadId()}
	// 既存 primary token の query handle だけを取得し、identity/token を生成しません。
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &out.token) != nil || out.token == 0 {
		return out, ErrDenied
	}
	return out, nil
}
func (p *nativeProcess) inspect() (processState, error) {
	if windows.GetCurrentThreadId() != p.thread {
		return processState{}, ErrFatal
	}
	alive, e := windows.WaitForSingleObject(p.process, 0)
	if e != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return processState{}, ErrDenied
	}
	pid, e := windows.GetProcessId(p.process)
	if e != nil || pid == 0 {
		return processState{}, ErrDenied
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(p.process, &creation, &exit, &kernel, &user) != nil {
		return processState{}, ErrDenied
	}
	ident, e := tokenIdentity(p.token)
	if e != nil {
		return processState{}, e
	}
	path, e := processPath(p.process)
	if e != nil {
		return processState{}, e
	}
	alive, e = windows.WaitForSingleObject(p.process, 0)
	if e != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return processState{}, ErrDenied
	}
	return processState{pid: pid, creation: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), alive: true, path: path, identity: ident}, nil
}

// close は取得済み handle の解放専用で、thread の再認証を行いません。
func (p *nativeProcess) close() error {
	if p.token != 0 {
		if p.token.Close() == nil {
			p.token = 0
		}
	}
	if p.process != 0 {
		if windows.CloseHandle(p.process) == nil {
			p.process = 0
		}
	}
	if p.token != 0 || p.process != 0 {
		return ErrUnconfirmed
	}
	return nil
}
