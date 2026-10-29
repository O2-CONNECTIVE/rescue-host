package guestaccount

import (
	"sort"
	"strconv"
	"strings"
)

const permissionMaxGroups = 64
const permissionMaxEdges = 256

type permissionGroup struct {
	sid     string
	members []string
}

// 形式検査であり認証ではない。未知のSIDも観測可能なので型だけを検査する。
func permissionSID(s string) bool {
	p := strings.Split(s, "-")
	if len(p) < 4 || len(p) > 18 || p[0] != "S" || p[1] != "1" {
		return false
	}
	for i, v := range p[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, e := strconv.ParseUint(v, 10, bits)
		if e != nil || strconv.FormatUint(n, 10) != v {
			return false
		}
	}
	return true
}
func containsPermissionSID(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// 静的割当の投影。logon SID等の未来の実token全体を列挙したとはしない。
// primary 513は呼出側が同じUSER_INFO_4から観測した場合だけ支持する。
func permissionClosure(b binding, groups []permissionGroup, logonSID string) ([]string, error) {
	if !accountSID(b.sid) || !machineSID(b.machine) || !strings.HasPrefix(b.sid, b.machine+"-") || logonSID != "S-1-5-2" {
		return nil, ErrDenied
	}
	if len(groups) > permissionMaxGroups {
		return nil, ErrDenied
	}
	seen := map[string]bool{}
	edges := 0
	for _, g := range groups {
		if !permissionSID(g.sid) || seen[g.sid] || (!strings.HasPrefix(g.sid, "S-1-5-32-") && !strings.HasPrefix(g.sid, b.machine+"-")) {
			return nil, ErrDenied
		}
		seen[g.sid] = true
		members := map[string]bool{}
		for _, m := range g.members {
			edges++
			if edges > permissionMaxEdges || !permissionSID(m) || members[m] {
				return nil, ErrDenied
			}
			members[m] = true
		}
	}
	if !seen["S-1-5-32-545"] {
		return nil, ErrDenied
	}
	closure := map[string]bool{b.sid: true, b.machine + "-513": true, "S-1-1-0": true, "S-1-2-0": true, "S-1-5-11": true, "S-1-5-113": true, "S-1-5-15": true, logonSID: true}
	for pass := 0; pass <= len(groups); pass++ {
		changed := false
		for _, g := range groups {
			if closure[g.sid] {
				continue
			}
			for _, m := range g.members {
				if closure[m] {
					// 初版はUsersと既観測primary以外のalias経由を支持しない。
					if g.sid != "S-1-5-32-545" && g.sid != b.machine+"-513" {
						return nil, ErrDenied
					}
					closure[g.sid] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	out := make([]string, 0, len(closure))
	for sid := range closure {
		out = append(out, sid)
	}
	sort.Strings(out)
	return out, nil
}
