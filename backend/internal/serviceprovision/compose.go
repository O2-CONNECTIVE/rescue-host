package serviceprovision

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// ComposeServiceは保護供給元の所有primary handleを借りる構成段階だけを担う。
// NativeExecutionは保護検査後に別途取得したhandleだけをこの構成へ借用する。
// 呼出元はserviceの全回収までhandleを所有し、未確認時に閉じてはならない。
// 成功は純粋構成だけであり、record/handle/承認sourceの信頼証明ではない。
func ComposeService(r Record, token uintptr, approval servicehost.ProtectedProvisioning) (*servicehost.Service, error) {
	if err := ValidateRecord(r); err != nil {
		return nil, err
	}
	if approval == nil {
		return nil, ErrApprovalUnconfigured
	}
	base, err := servicehost.NewNativeStdioProvisioning(servicehost.NativeStdioConfig{Profile: r.Profile, PrimaryToken: token, PolicyRef: r.PolicyRef, PollInterval: r.PollInterval, OperationTimeout: r.OperationTimeout, CleanupTimeout: r.StdioCleanupTimeout, StderrBytes: r.StderrBytes})
	if err != nil {
		return nil, err
	}
	child, err := servicehost.NewNativeChildProvisioning(base, servicehost.NativeBootstrapConfig{Profile: r.Profile, PrimaryToken: token, Backend: r.Backend, HostKeyRef: r.HostKeyRef, Limits: r.Limits})
	if err != nil {
		return nil, err
	}
	return servicehost.NewService(servicehost.ServiceConfig{Ready: serviceready.Config{Profile: r.Profile, PrimaryToken: token, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout}, Provisioning: approval, Launcher: servicehost.NewJobRuntime(child), Budgets: r.Budgets})
}
