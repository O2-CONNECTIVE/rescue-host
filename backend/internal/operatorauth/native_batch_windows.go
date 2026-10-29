//go:build windows

package operatorauth

import (
	"context"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"golang.org/x/sys/windows"
)

// Service 登録後の Start から全回収まで、caller は同じ固定 worker に留まる。
// 保護 Factory は service 自身だけを証明し、controller 認証へ流用しない。
func startNativeBatch(ctx context.Context, s nativeBatchSpec, factory *bootstrapauth.Factory, servicePrimaryToken uintptr, owner batchOwner) (*nativeBatch, error) {
	b, err := prepareNativeBatch(s, factory, servicePrimaryToken, owner)
	if err != nil {
		return nil, err
	}
	return b, b.Start(ctx)
}

// prepare は保護 Factory と Service 自身の借用 primary token を pure 保持するだけ。
// helper 起動用 s.launch.primaryToken は service 認証へ補完しない。
// scope は Service 登録後の Start admission 内で worker 自身が取得する。
func prepareNativeBatch(s nativeBatchSpec, factory *bootstrapauth.Factory, servicePrimaryToken uintptr, owner batchOwner) (*nativeBatch, error) {
	if factory == nil || s.producer == nil {
		return nil, ErrUnconfigured
	}
	thread := &nativeThread{}
	scope, err := newBatchScope(servicePrimaryToken, batchScopeOps{
		threadID: windows.GetCurrentThreadId,
		clean:    thread.clean,
		verify: func(c context.Context, token uintptr) (batchServiceScope, error) {
			thread.owner = windows.GetCurrentThreadId()
			p, e := factory.VerifyService(c, token)
			if p == nil {
				return nil, e
			}
			return p, e
		},
	})
	if err != nil {
		return nil, err
	}
	b, err := prepareBatch(s, owner, nativeBatchOps{
		scope:         scope,
		moveSoleInput: moveNativeSoleInput,
		parent:        func(context.Context) error { return nil },
		clean:         func() error { return nil },
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
	if b != nil {
		b.serviceFactory = factory
	}
	return b, err
}

func validateNativeOperatorRead(h uintptr) error {
	if !helperHandle(h) {
		return ErrDenied
	}
	var flags uint32
	ok, _, _ := reportHandleInfo.Call(h, uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || flags != 0 {
		return ErrDenied
	}
	kind, e := windows.GetFileType(windows.Handle(h))
	if e != nil || kind != windows.FILE_TYPE_PIPE {
		return ErrDenied
	}
	var info [14]uint32
	var size uint32
	status, _, _ := reportQueryObject.Call(h, 0, uintptr(unsafe.Pointer(&info[0])), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&size)))
	if status != 0 || size != uint32(unsafe.Sizeof(info)) || info[1] != 0x120089 {
		return ErrDenied
	}
	var pipeFlags, mode, pid uint32
	if windows.GetNamedPipeInfo(windows.Handle(h), &pipeFlags, nil, nil, nil) != nil || pipeFlags != 5 {
		return ErrDenied
	}
	if windows.GetNamedPipeHandleState(windows.Handle(h), &mode, nil, nil, nil, nil, 0) != nil || mode != windows.PIPE_READMODE_MESSAGE {
		return ErrDenied
	}
	if windows.GetNamedPipeServerProcessId(windows.Handle(h), &pid) != nil || pid != windows.GetCurrentProcessId() {
		return ErrDenied
	}
	return nil
}

func nativeDuplicateOperatorRead(h uintptr) (uintptr, error) {
	if e := validateNativeOperatorRead(h); e != nil {
		return 0, e
	}
	var copy windows.Handle
	e := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(h), windows.CurrentProcess(), &copy, 0x120089, true, 0)
	return uintptr(copy), e
}
