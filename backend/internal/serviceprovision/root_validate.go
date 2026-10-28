package serviceprovision

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (a Anchor) Validate() error {
	if len(a.ServiceName) == 0 || len(a.ServiceName) > 128 || !rootPath(a.RecordPath) || !rootPath(a.DigestPath) || strings.EqualFold(a.RecordPath, a.DigestPath) || len(a.Volume.Root) != 3 || a.Volume.Root[1:] != `:\` || a.Volume.Serial == 0 || (a.Volume.Filesystem != "NTFS" && a.Volume.Filesystem != "ReFS") || !strings.HasPrefix(a.RecordPath, a.Volume.Root) || !strings.HasPrefix(a.DigestPath, a.Volume.Root) || a.ReadTimeout <= 0 || a.CleanupTimeout <= 0 {
		return ErrRootConfig
	}
	for _, c := range a.ServiceName {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ErrRootConfig
		}
	}
	x, y := strings.ToLower(a.RecordPath), strings.ToLower(a.DigestPath)
	if strings.HasPrefix(x, y+`\`) || strings.HasPrefix(y, x+`\`) {
		return ErrRootConfig
	}
	return nil
}
func rootPath(s string) bool {
	if !utf8.ValidString(s) || len(s) <= 3 || len(s) > 1024 || s[0] < 'A' || s[0] > 'Z' || s[1:3] != `:\` {
		return false
	}
	parts := strings.Split(s[3:], `\`)
	if len(parts) > 31 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") {
			return false
		}
		for _, c := range p {
			if c < 32 || strings.ContainsRune(`/:<>"|?*`, c) {
				return false
			}
		}
		stem := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
			return false
		}
	}
	return true
}
func rootPaths(path string) []string {
	paths := []string{path[:3]}
	rest := strings.Split(path[3:], `\`)
	current := path[:3]
	for i, part := range rest {
		if i > 0 {
			current += `\`
		}
		current += part
		paths = append(paths, current)
	}
	return paths
}
func rootServiceIdentifier(s string) bool {
	return strings.HasPrefix(s, "S-1-5-80-") && len(strings.Split(strings.TrimPrefix(s, "S-1-5-80-"), "-")) == 5 && rootSID(s)
}
func rootSID(s string) bool {
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
func rootTrusted(serviceSID, sid string) bool {
	return sid == "S-1-5-18" || sid == "S-1-5-32-544" || sid == serviceSID
}
func rootSafeACL(serviceSID string, acl []protectedkey.ACE) bool {
	if acl == nil || len(acl) > 128 {
		return false
	}
	for _, a := range acl {
		if a.Type > 1 || a.Flags & ^uint8(0x1f) != 0 || a.Mask & ^uint32(0xf01f01ff) != 0 || !rootSID(a.SID) {
			return false
		}
		if a.Type == 1 || a.Flags&8 != 0 || rootTrusted(serviceSID, a.SID) {
			continue
		}
		mask := a.Mask & 0x1f01ff
		for _, p := range [][2]uint32{{0x80000000, 0x120089}, {0x40000000, 0x120116}, {0x20000000, 0x1200a0}, {0x10000000, 0x1f01ff}} {
			if a.Mask&p[0] != 0 {
				mask |= p[1]
			}
		}
		if mask&0xd0156 != 0 {
			return false
		}
	}
	return true
}
