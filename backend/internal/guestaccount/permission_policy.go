package guestaccount

import "strings"

// 初版の保持済みstaging対象。将来のruntime/Jobを取得済みとはしない。
type permissionRole uint8

const (
	roleService permissionRole = iota + 1
	roleSupervisorProcess
	roleSupervisorToken
	roleControlPipe
	roleConfiguration
	roleJournal
	roleHostKey
	roleArtifact
	roleAncestor
)

func permissionObjectSafe(b binding, serviceSID string, s permissionObjectSnapshot) bool {
	if !strings.HasPrefix(serviceSID, "S-1-5-80-") || !serviceIdentity(serviceSID) {
		return false
	}
	trusted := func(sid string) bool {
		return sid != "" && sid != b.sid && (sid == b.service || sid == serviceSID || sid == b.operator || sid == "S-1-5-18" || sid == "S-1-5-32-544")
	}
	if !trusted(s.owner) || s.acl == nil || len(s.acl) > 128 {
		return false
	}
	known, readonly := uint32(0xf01f01ff), uint32(0x20000)
	switch s.kind {
	case permissionObjectService:
		if s.role != roleService {
			return false
		}
		known = 0xf00f01ff
		readonly = 0x2008d
	case permissionObjectProcess:
		if s.role != roleSupervisorProcess {
			return false
		}
		known = 0xf01fffff
		readonly = 0x120000
	case permissionObjectToken:
		if s.role != roleSupervisorToken {
			return false
		}
	case permissionObjectPipe:
		if s.role != roleControlPipe {
			return false
		}
		readonly = 0x120000
	case permissionObjectDirectory:
		if s.role != roleAncestor {
			return false
		}
		readonly = 0x1200a9
	case permissionObjectFile:
		if s.role < roleConfiguration || s.role > roleArtifact {
			return false
		}
		readonly = 0x120089
		if s.role == roleHostKey {
			readonly = 0x120088
		}
		if s.role == roleArtifact {
			readonly = 0x1200a9
		}
	default:
		return false
	}
	for _, a := range s.acl {
		if a.kind > 1 || a.flags & ^uint8(0x1f) != 0 || a.mask & ^known != 0 || !permissionSID(a.sid) {
			return false
		}
		if s.kind != permissionObjectFile && s.kind != permissionObjectDirectory && a.flags != 0 {
			return false
		}
		if a.kind == 1 || a.flags&8 != 0 || trusted(a.sid) {
			continue
		}
		// 型別の明示readonlyだけを許し、GENERICやDENY相殺で広げない。
		if a.mask&0xf0000000 != 0 || a.mask & ^readonly != 0 {
			return false
		}
	}
	return true
}

func permissionObjectsConfigured(b binding, serviceSID string, objects []permissionObject) bool {
	if !strings.HasPrefix(serviceSID, "S-1-5-80-") || !serviceIdentity(serviceSID) || serviceSID == b.sid || len(objects) < 8 || len(objects) > 32 {
		return false
	}
	counts := map[permissionRole]int{}
	handles := map[uintptr]bool{}
	for _, o := range objects {
		if !ordinaryPermissionObject(o) || handles[o.handle] {
			return false
		}
		handles[o.handle] = true
		counts[o.role]++
		s := permissionObjectSnapshot{role: o.role, kind: o.kind, owner: b.service, acl: []journalACE{}}
		if !permissionObjectSafe(b, serviceSID, s) {
			return false
		}
		if o.kind == permissionObjectToken && o.userSID != b.service {
			return false
		}
	}
	for _, r := range []permissionRole{roleService, roleSupervisorProcess, roleSupervisorToken} {
		if counts[r] != 1 {
			return false
		}
	}
	for _, r := range []permissionRole{roleConfiguration, roleJournal, roleHostKey, roleArtifact, roleAncestor} {
		if counts[r] < 1 {
			return false
		}
	}
	return counts[roleControlPipe] <= 1
}

func permissionRights(p Plan, rights []string) (privileges, logons []string, err error) {
	if !labels(rights, true) {
		return nil, nil, ErrDenied
	}
	privileges = []string{}
	logons = []string{}
	for _, r := range rights {
		switch r {
		case "SeChangeNotifyPrivilege", "SeIncreaseWorkingSetPrivilege", "SeTimeZonePrivilege":
			privileges = append(privileges, r)
		case "SeNetworkLogonRight", "SeInteractiveLogonRight", "SeDenyNetworkLogonRight", "SeDenyInteractiveLogonRight", "SeDenyRemoteInteractiveLogonRight", "SeDenyBatchLogonRight", "SeDenyServiceLogonRight":
			logons = append(logons, r)
		default:
			return nil, nil, ErrDenied
		}
	}
	if !subset(privileges, p.policy.privileges) || !subset(logons, p.policy.logonRights) {
		return nil, nil, ErrDenied
	}
	return privileges, logons, nil
}
