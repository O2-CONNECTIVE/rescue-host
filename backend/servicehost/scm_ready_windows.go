//go:build windows

package servicehost

import (
	"time"

	"golang.org/x/sys/windows/svc"
)

type readySCMHandler struct{ service *Service }

// NewSCMReadyHandlerは設定/登録/起動をせず、明示構成済serviceのSCM adapterだけを返す。
// Executeはsvc.Runが所有する専用goroutineで使用し、手動呼出しの成功を実SCM配送と呼ばない。
func NewSCMReadyHandler(service *Service) svc.Handler { return &readySCMHandler{service} }
func (h *readySCMHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	return serveReadySCM(h.service, requests, statuses, decodeReadyControl, func(state ServiceState) svc.Status { return readySCMStatus(h.service, state) })
}
func decodeReadyControl(request svc.ChangeRequest) Control {
	switch request.Cmd {
	case svc.Stop:
		return Stop
	case svc.Shutdown:
		return Shutdown
	case svc.Interrogate:
		return Interrogate
	default:
		return Ignore
	}
}
func readySCMStatus(service *Service, state ServiceState) svc.Status {
	status := svc.Status{State: svc.StopPending, CheckPoint: 2}
	var startup, cleanup time.Duration
	if service != nil {
		startup = service.cfg.Budgets.Startup
		cleanup = service.cfg.Budgets.Cleanup
	}
	switch state {
	case StartPending:
		status.State = svc.StartPending
		status.CheckPoint = 1
		status.WaitHint = scmWaitHint(startup)
	case Running:
		status.State = svc.Running
		status.CheckPoint = 0
		status.Accepts = svc.AcceptStop | svc.AcceptShutdown
	default:
		status.WaitHint = scmWaitHint(cleanup)
	}
	return status
}
func scmWaitHint(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	ms := d.Milliseconds()
	if ms < 1 {
		return 1
	}
	if ms > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(ms)
}

// RunReadySCMは将来の保護されたservice executable専用。既定RunSCM/mainは呼ばない。
// service登録/OS権限変更を含まず、実行には対象/変更/復元を示した別承認が必要。
func RunReadySCM(service *Service) error {
	if service == nil || service.serviceName == "" || service.callerGuard == nil {
		return ErrProvisioningUnconfigured
	}
	return runReadySCMDispatch(service, func() error { return svc.Run(service.serviceName, NewSCMReadyHandler(service)) })
}
