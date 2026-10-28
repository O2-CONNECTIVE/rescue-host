//go:build windows

package operatorauth

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// nativeProcessは事前保持したhelper handleだけを所有し、PIDから開き直さない。
// terminate/waitは並行可。closeは両操作の参照が終わるまで解放しない。
type nativeProcess struct {
	mu     sync.RWMutex
	handle uintptr
}

const nativeStillActive = 259

func (p *nativeProcess) terminate() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !nativeRealHandle(p.handle) || windows.TerminateProcess(windows.Handle(p.handle), 1) != nil {
		return ErrUnconfirmed
	}
	return nil
}

func (p *nativeProcess) wait(ctx context.Context) (uint32, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if ctx == nil || !nativeRealHandle(p.handle) {
		return 0, ErrUnconfirmed
	}
	deadline, finite := ctx.Deadline()
	if !finite {
		return 0, ErrUnconfirmed
	}
	for ctx.Err() == nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, ErrUnconfirmed
		}
		poll := min(remaining, 20*time.Millisecond)
		status, err := windows.WaitForSingleObject(windows.Handle(p.handle), uint32(poll/time.Millisecond))
		if err != nil {
			return 0, ErrUnconfirmed
		}
		if status == windows.WAIT_OBJECT_0 {
			var exit uint32
			if windows.GetExitCodeProcess(windows.Handle(p.handle), &exit) != nil || exit == nativeStillActive || ctx.Err() != nil || !time.Now().Before(deadline) {
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

// close失敗はhandleを保持し、解放済みへ変換しない。
func (p *nativeProcess) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 {
		return nil
	}
	if !nativeRealHandle(p.handle) || windows.CloseHandle(windows.Handle(p.handle)) != nil {
		return ErrUnconfirmed
	}
	p.handle = 0
	return nil
}
