//go:build windows

package operatorauth

import (
	"context"
	"errors"
	"sync"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"golang.org/x/sys/windows"
)

// scopeは同じ固定caller threadで事前取得・保持したservice自身のnative証拠。
// controller server認証やsole-readの移譲元を証明するものではない。
// callerはBatch全回収までthreadとscopeを保持する。固定provisioning未接続の
// RunFixedからは呼ばれず、UI/argv/SID文字列からscopeを生成しない。
func startNativeBatch(ctx context.Context, s nativeBatchSpec, scope *bootstrapauth.ServiceScope, owner batchOwner) (*nativeBatch, error) {
	b, err := prepareNativeBatch(s, scope, owner)
	if err != nil {
		return nil, err
	}
	return b, b.Start(ctx)
}

// prepareNativeBatch constructs only the same retained owner. No thread query,
// token/pipe acquisition, or launch occurs before Service can register it.
func prepareNativeBatch(s nativeBatchSpec, scope *bootstrapauth.ServiceScope, owner batchOwner) (*nativeBatch, error) {
	if scope == nil || s.producer == nil {
		return nil, ErrUnconfigured
	}
	thread := &nativeThread{}
	var bind sync.Once
	bindThread := func() { bind.Do(func() { thread.owner = windows.GetCurrentThreadId() }) }
	return prepareBatch(s, owner, nativeBatchOps{
		parent: func(c context.Context) error {
			bindThread()
			if e := scope.Recheck(c); e != nil {
				if errors.Is(e, bootstrapauth.ErrFatal) {
					return errors.Join(ErrFatal, e)
				}
				return e
			}
			return nil
		},
		clean: func() error { bindThread(); return thread.clean() },
		newProfile: func(c context.Context) (batchProfile, error) {
			p, e := newNativeProfilePipe(c, s.producer)
			if p == nil {
				return nil, e
			}
			return p, e
		},
		newReport: func(c context.Context, r reportConfig) (batchReport, error) {
			p, e := newNativeReportPair(c, r)
			if p == nil {
				return nil, e
			}
			return p, e
		},
		duplicateRead: nativeDuplicateOperatorRead,
		start: func(c context.Context, l nativeLaunchSpec) (batchChild, error) {
			p, e := startNativeHelper(c, l)
			if p == nil {
				return nil, e
			}
			return p, e
		},
		closeHandle: func(h uintptr) error { return windows.CloseHandle(windows.Handle(h)) },
	})
}

func nativeDuplicateOperatorRead(h uintptr) (uintptr, error) {
	if !helperHandle(h) {
		return 0, ErrDenied
	}
	var flags uint32
	ok, _, _ := reportHandleInfo.Call(h, uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || flags != 0 {
		return 0, ErrDenied
	}
	kind, e := windows.GetFileType(windows.Handle(h))
	if e != nil || kind != windows.FILE_TYPE_PIPE {
		return 0, ErrDenied
	}
	var info [14]uint32
	var size uint32
	status, _, _ := reportQueryObject.Call(h, 0, uintptr(unsafe.Pointer(&info[0])), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&size)))
	if status != 0 || size != uint32(unsafe.Sizeof(info)) || info[1] != 0x120089 {
		return 0, ErrDenied
	}
	var pipeFlags, mode, pid uint32
	if windows.GetNamedPipeInfo(windows.Handle(h), &pipeFlags, nil, nil, nil) != nil || pipeFlags != 5 {
		return 0, ErrDenied
	}
	if windows.GetNamedPipeHandleState(windows.Handle(h), &mode, nil, nil, nil, nil, 0) != nil || mode != windows.PIPE_READMODE_MESSAGE {
		return 0, ErrDenied
	}
	if windows.GetNamedPipeServerProcessId(windows.Handle(h), &pid) != nil || pid != windows.GetCurrentProcessId() {
		return 0, ErrDenied
	}
	var copy windows.Handle
	e = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(h), windows.CurrentProcess(), &copy, 0x120089, true, 0)
	return uintptr(copy), e
}
