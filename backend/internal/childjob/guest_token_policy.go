package childjob

import (
	"strconv"
	"strings"
)

// GuestTokenExpectation is a frozen verification candidate, not authentication,
// account provenance or permission to create/log on an account. This deliberately
// supports only a narrow network-guest profile; no runtime profile is adopted.
type GuestTokenExpectation struct {
	user               string
	groups, privileges []string
	valid              bool
}

func guestAccountSID(user string) (string, bool) {
	parts := strings.Split(user, "-")
	if len(parts) != 8 || strings.Join(parts[:4], "-") != "S-1-5-21" {
		return "", false
	}
	for _, part := range parts[4:] {
		n, e := strconv.ParseUint(part, 10, 32)
		if e != nil || strconv.FormatUint(n, 10) != part {
			return "", false
		}
	}
	rid, _ := strconv.ParseUint(parts[7], 10, 32)
	return strings.Join(parts[:7], "-"), rid >= 1000
}
func allowedGuestGroup(sid, machine string) bool {
	switch sid {
	case "S-1-1-0", "S-1-2-0", "S-1-5-2", "S-1-5-11", "S-1-5-15", "S-1-5-113", "S-1-5-32-545", machine + "-513":
		return true
	}
	return false
}
func guestPrivilegeNames() [3]string {
	return [3]string{"SeChangeNotifyPrivilege", "SeIncreaseWorkingSetPrivilege", "SeTimeZonePrivilege"}
}
func allowedGuestPrivilege(name string) bool {
	for _, allowed := range guestPrivilegeNames() {
		if name == allowed {
			return true
		}
	}
	return false
}
func NewGuestTokenExpectation(user string, groups, privileges []string) (GuestTokenExpectation, error) {
	machine, ok := guestAccountSID(user)
	if !ok || groups == nil || privileges == nil || len(groups) > 64 || len(privileges) > 64 {
		return GuestTokenExpectation{}, ErrConfig
	}
	seen := map[string]bool{}
	for _, sid := range groups {
		if seen[sid] || !allowedGuestGroup(sid, machine) {
			return GuestTokenExpectation{}, ErrConfig
		}
		seen[sid] = true
	}
	for _, sid := range []string{"S-1-1-0", "S-1-5-2", "S-1-5-11", "S-1-5-32-545"} {
		if !seen[sid] {
			return GuestTokenExpectation{}, ErrConfig
		}
	}
	seen = map[string]bool{}
	for _, name := range privileges {
		if seen[name] || !allowedGuestPrivilege(name) {
			return GuestTokenExpectation{}, ErrConfig
		}
		seen[name] = true
	}
	return GuestTokenExpectation{user: user, groups: append([]string{}, groups...), privileges: append([]string{}, privileges...), valid: true}, nil
}
func (e GuestTokenExpectation) accepts(f guestTokenFacts) bool {
	machine, ok := guestAccountSID(e.user)
	if !e.valid || !ok || f.kind != 1 || f.user != e.user || f.owner != e.user || f.primaryGroup != machine+"-513" || f.authentication == 0 || f.tokenID == 0 || f.modifiedID == 0 || f.integrity != 0x2000 || f.elevationType != 1 || f.elevated || f.uiAccess || f.hasRestrictingSIDs || f.appContainer || f.groups == nil || f.privileges == nil || len(f.groups) > 64 || len(f.privileges) > 64 {
		return false
	}
	allowed := map[string]bool{}
	for _, sid := range e.groups {
		allowed[sid] = true
	}
	seen := map[string]bool{}
	logons, integrities := 0, 0
	for _, g := range f.groups {
		if seen[g.sid] {
			return false
		}
		seen[g.sid] = true
		if g.sid == "S-1-16-8192" && g.attributes == 0x60 {
			integrities++
			continue
		}
		if strings.HasPrefix(g.sid, "S-1-5-5-") && g.attributes&0xc0000000 == 0xc0000000 && g.attributes & ^uint32(0xc0000007) == 0 {
			parts := strings.Split(g.sid, "-")
			if len(parts) != 6 {
				return false
			}
			for _, v := range parts[4:] {
				n, err := strconv.ParseUint(v, 10, 32)
				if err != nil || strconv.FormatUint(n, 10) != v {
					return false
				}
			}
			logons++
			continue
		}
		// Disabled/deny-only privileged groups cannot be hidden by an enabled-only check.
		if !allowed[g.sid] || !allowedGuestGroup(g.sid, machine) || g.attributes & ^uint32(7) != 0 || g.attributes&4 == 0 {
			return false
		}
	}
	if logons > 1 || integrities > 1 {
		return false
	}
	for _, sid := range e.groups {
		if !seen[sid] {
			return false
		}
	}
	allowed = map[string]bool{}
	for _, name := range e.privileges {
		allowed[name] = true
	}
	seen = map[string]bool{}
	for _, p := range f.privileges {
		// Presence matters even when the privilege is currently disabled.
		if !allowed[p.name] || !allowedGuestPrivilege(p.name) || seen[p.name] || p.attributes & ^uint32(0x80000003) != 0 {
			return false
		}
		seen[p.name] = true
	}
	return true
}
