package bootstrapauth

import (
	"strconv"
	"strings"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

func validImage(p Profile, image Image) bool {
	if image.MaxBytes <= 0 || image.SHA256 == [32]byte{} || !canonicalPath(image.Path) || !root(image.Volume.Root) || !strings.HasPrefix(image.Path, image.Volume.Root) || image.Volume.Serial == 0 || (image.Volume.Filesystem != "NTFS" && image.Volume.Filesystem != "ReFS") {
		return false
	}
	parts := strings.Split(image.Path[3:], `\`)
	if len(parts)+1 != len(image.Nodes) || len(image.Nodes) > 32 {
		return false
	}
	path := image.Volume.Root
	for i, n := range image.Nodes {
		if i > 0 {
			if i > 1 {
				path += `\`
			}
			path += parts[i-1]
		}
		if n.Path != path || !trusted(p, n.OwnerSID) || !safeACL(p, n.ACL) {
			return false
		}
	}
	return true
}
func trusted(p Profile, s string) bool {
	return s != "" && s != p.Keys.GuestSID && (s == p.Keys.ProcessUserSID || s == p.Keys.ServiceSID || s == p.Keys.OperatorSID || s == "S-1-5-18" || s == "S-1-5-32-544")
}
func safeACL(p Profile, acl []protectedkey.ACE) bool {
	if acl == nil || len(acl) > 128 {
		return false
	}
	for _, a := range acl {
		if a.Type > 1 || a.Flags & ^uint8(0x1f) != 0 || a.Mask & ^uint32(0xf01f01ff) != 0 || !validSID(a.SID) {
			return false
		}
		// DENY で危険な ALLOW を相殺しません。公開 exe の読取・実行は許します。
		if a.Type == 0 && a.Flags&8 == 0 && !trusted(p, a.SID) && expand(a.Mask)&0x000d0156 != 0 {
			return false
		}
	}
	return true
}
func expand(mask uint32) uint32 {
	n := mask & 0x001f01ff
	for _, p := range [][2]uint32{{0x80000000, 0x00120089}, {0x40000000, 0x00120116}, {0x20000000, 0x001200a0}, {0x10000000, 0x001f01ff}} {
		if mask&p[0] != 0 {
			n |= p[1]
		}
	}
	return n
}
func validSID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 4 || len(parts) > 18 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for i, s := range parts[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, e := strconv.ParseUint(s, 10, bits)
		if e != nil || strconv.FormatUint(n, 10) != s {
			return false
		}
	}
	return true
}
func root(s string) bool { return len(s) == 3 && s[0] >= 'A' && s[0] <= 'Z' && s[1:] == `:\` }
func canonicalPath(s string) bool {
	if len(s) <= 3 || len(s) > 1024 || !root(s[:3]) {
		return false
	}
	for _, part := range strings.Split(s[3:], `\`) {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		for _, c := range part {
			if c < 32 || strings.ContainsRune(`/:<>"|?*`, c) {
				return false
			}
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
			return false
		}
	}
	return true
}
func serviceName(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func copyNodes(nodes []protectedkey.NodePolicy) []protectedkey.NodePolicy {
	out := append([]protectedkey.NodePolicy(nil), nodes...)
	for i := range out {
		out[i].ACL = append(make([]protectedkey.ACE, 0, len(out[i].ACL)), out[i].ACL...)
	}
	return out
}
func copyProfile(p Profile) Profile {
	catalog := make(map[string]protectedkey.KeyProfile, len(p.Keys.Catalog))
	for ref, key := range p.Keys.Catalog {
		key.Nodes = copyNodes(key.Nodes)
		catalog[ref] = key
	}
	p.Keys.Catalog = catalog
	p.ChildImage.Nodes = copyNodes(p.ChildImage.Nodes)
	p.SupervisorImage.Nodes = copyNodes(p.SupervisorImage.Nodes)
	return p
}
