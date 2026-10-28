//go:build windows

package servicehost

import "golang.org/x/sys/windows/svc"

// 名前はコード準備用。service 導入や per-service SID の採用決定ではない。
const scmServiceName = "RescueHostSupervisor"

// RunSCM は将来の service executable の entry。製品入口と試験からは呼ばない。
// 実機実行は対象・変更・復元手順を示した別承認が必要。登録操作は含まない。
// 保護 provisioning は必ず未設定とし、console/debug へ fallback しない。
func RunSCM() error {
	if err := svc.Run(scmServiceName, NewSCMHandler(nil)); err != nil {
		return ErrSCMUnavailable
	}
	// dispatcher 終了を、未設定 host の起動成功に変換しない。
	return ErrProvisioningUnconfigured
}

type scmHandler struct{ host *Host }

// NewSCMHandler は登録・起動・token 取得をせず Handler だけを返す。
// Execute の直接試験は実 SCM の配送・状態報告の成功証明ではない。
func NewSCMHandler(host *Host) svc.Handler { return &scmHandler{host: host} }

func (h *scmHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	err := serve(h.host, requests, statuses, func(request svc.ChangeRequest) Control {
		// args/CurrentStatus/EventData/Context を認可や policy 選択に利用しない。
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
	}, func(state ServiceState) svc.Status {
		status := svc.Status{State: svc.StopPending}
		if state == StartPending {
			status.State = svc.StartPending
		}
		// 未設定なので Running は報告しない。Start/StopPending の Accepts は0。
		// startup 中の Stop 配送は実 SCM で未検証。予算/WaitHint は未決。
		return status
	})
	if err != nil {
		// 内部 error、path、token、鍵を公開診断へ転送しない。
		return true, 1
	}
	return false, 0
}
