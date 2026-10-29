package guestaccount

import "strings"

func validFixedLayout(w fixedLayoutWire, f *FixedLayout) bool {
	c := f.profile
	if w.Protocol != "rescue-guest-provisioning/1" || c.installation == ([32]byte{}) || !machineSID(f.machine) || !accountSID(f.operator) || !serviceIdentity(f.user) || !strings.HasPrefix(c.serviceSID, "S-1-5-80-") || !serviceIdentity(c.serviceSID) || !stagingServiceName(c.serviceName) {
		return false
	}
	// token/stdioやsessionはまだ取得しない。純粋なfile配置の検査だけを共用する。
	if c.native.image.SHA256 == ([32]byte{}) || c.native.manifest.SHA256 == ([32]byte{}) || c.native.assembly.SHA256 == ([32]byte{}) || !stagingFiles(c) || !fixedLayoutPowerShell(c.native) {
		return false
	}
	return fixedLayoutPolicyAllowed(f.machine, f.policy)
}
func fixedLayoutPolicyAllowed(machine string, p limitedPolicy) bool {
	required := []string{machine + "-513", "S-1-1-0", "S-1-2-0", "S-1-5-11", "S-1-5-113", "S-1-5-15", "S-1-5-2", "S-1-5-32-545"}
	if !p.known || administrativePolicy(p) || len(p.groups) != len(required) || !labels(p.groups, false) || !labels(p.privileges, true) || !labels(p.logonRights, true) {
		return false
	}
	for _, group := range p.groups {
		if !permissionSID(group) || !containsPermissionSID(required, group) {
			return false
		}
	}
	for _, privilege := range p.privileges {
		switch privilege {
		case "SeChangeNotifyPrivilege", "SeIncreaseWorkingSetPrivilege", "SeTimeZonePrivilege":
		default:
			return false
		}
	}
	for _, right := range p.logonRights {
		switch right {
		case "SeNetworkLogonRight", "SeInteractiveLogonRight", "SeDenyNetworkLogonRight", "SeDenyInteractiveLogonRight", "SeDenyRemoteInteractiveLogonRight", "SeDenyBatchLogonRight", "SeDenyServiceLogonRight":
		default:
			return false
		}
	}
	return true
}

// native factoryが固定する既存module pairとimage構文を、取得なしで照合する。
func fixedLayoutPowerShell(p sidNativeProfile) bool {
	const manifest = `\Microsoft.PowerShell.LocalAccounts.psd1`
	const assembly = `\Microsoft.PowerShell.LocalAccounts.dll`
	return sidRunImage(p.image.Path) && strings.HasSuffix(p.manifest.Path, manifest) && strings.HasSuffix(p.assembly.Path, assembly) && strings.TrimSuffix(p.manifest.Path, manifest) == strings.TrimSuffix(p.assembly.Path, assembly)
}
