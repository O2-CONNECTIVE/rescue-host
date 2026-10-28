package serviceprovision

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func launchTrusted(c serviceready.Config, sid string) bool {
	p := c.Profile.Keys
	return sid != "" && sid != p.GuestSID && (sid == p.ProcessUserSID || sid == p.ServiceSID || sid == p.OperatorSID || sid == "S-1-5-18" || sid == "S-1-5-32-544")
}

// trustedは対象object全権を許す固定policy。genericを未知主体のreadonlyへ流用しない。
func launchACL(c serviceready.Config, acl []protectedkey.ACE, kind uint8) bool {
	if acl == nil || len(acl) > 128 {
		return false
	}
	known, readonly := uint32(0xf00f01ff), uint32(0x20000)
	if kind != 0 {
		known = 0xf01fffff
	}
	if kind == 1 {
		readonly = 0x120000
	}
	for _, a := range acl {
		if a.Type > 1 || a.Flags & ^uint8(0x1f) != 0 || a.Mask & ^known != 0 || !rootSID(a.SID) {
			return false
		}
		if a.Type == 0 && !launchTrusted(c, a.SID) && (a.Mask & ^readonly != 0 || a.Mask&0xf0000000 != 0) {
			return false
		}
	}
	return true
}
func launchProtectionSafe(c serviceready.Config, p launchProtection) bool {
	return launchTrusted(c, p.object.owner) && launchACL(c, p.object.acl, 0) && launchTrusted(c, p.defaultOwner) && launchACL(c, p.defaultACL, 2)
}
func launchCloneACL(acl []protectedkey.ACE) []protectedkey.ACE {
	if acl == nil {
		return nil
	}
	return append(make([]protectedkey.ACE, 0, len(acl)), acl...)
}
func launchCloneProtection(p launchProtection) launchProtection {
	p.object.acl = launchCloneACL(p.object.acl)
	p.defaultACL = launchCloneACL(p.defaultACL)
	return p
}
func launchCloneThread(s launchThreadState) launchThreadState {
	s.security.acl = launchCloneACL(s.security.acl)
	return s
}
