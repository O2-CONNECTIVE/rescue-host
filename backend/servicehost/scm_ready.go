package servicehost

import (
	"context"
	"runtime"
)

// rejectUnusedはnative owner作成前の拒否専用。既存serviceの状態を上書きしない。
func (s *Service) rejectUnused() {
	s.mu.Lock()
	if s.used || s.started {
		s.mu.Unlock()
		return
	}
	s.used, s.stopping, s.cleanupConfirmed = true, true, true
	s.state = Stopped
	done := s.done
	s.mu.Unlock()
	if done != nil {
		s.finish.Do(func() { close(done) })
	}
}

// serveReadySCMはservice専用SCM entry。operator認証には使わない。
// Execute callerは専用goroutineを使用し、Fatal guard後はUnlockせずreturnして退役する。
func serveReadySCM[Request, Status any](service *Service, requests <-chan Request, statuses chan<- Status, decode func(Request) Control, encode func(ServiceState) Status) (specific bool, code uint32) {
	if service == nil {
		return true, 1
	}
	service.mu.Lock()
	guard, used, done := service.callerGuard, service.used, service.done
	budget := service.cfg.Budgets.Startup
	service.mu.Unlock()
	if guard == nil || used || done == nil || budget <= 0 {
		return true, 1
	}
	runtime.LockOSThread()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	err := guard(ctx)
	guardErr := ctx.Err()
	cancel()
	if !fatalServiceThread(err) {
		runtime.UnlockOSThread()
	}
	if err != nil || guardErr != nil {
		service.rejectUnused()
		return true, 1
	}
	// 元callerを先に拒否したうえで、full native検査を専用owner threadで行う。
	result := make(chan error, 1)
	go func() { result <- serveService(context.Background(), service, requests, statuses, decode, encode) }()
	err = <-result
	if service.Snapshot().CleanupConfirmed {
		if err != nil {
			return true, 1
		}
		return false, 0
	}
	// fatal ownerは退役済み。unknown資源はServiceに保持し、SCMへStoppedを返さない。
	// このhold loopは新規認証/発行/activationをせず、管理者の外部介入を要する。
	pending := true
	for {
		var output chan<- Status
		if pending {
			output = statuses
		}
		select {
		case output <- encode(StopPending):
			pending = false
		case request, open := <-requests:
			if !open {
				requests = nil
				continue
			}
			switch decode(request) {
			case Stop, Shutdown, Interrogate:
				pending = true
			}
		}
	}
}
