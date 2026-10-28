package protectedkey

import (
	"encoding/base64"
	"strconv"
	"strings"
)

func validateProfile(p Profile) error {
	if p.Version != 1 || len(p.Catalog) == 0 || len(p.Catalog) > 8 || !processSID(p.ProcessUserSID) || !specificSID(p.ServiceSID, "S-1-5-80-", 5) || !specificSID(p.OperatorSID, "S-1-5-21-", 4) || !specificSID(p.GuestSID, "S-1-5-21-", 4) || p.GuestSID == p.OperatorSID || p.GuestSID == p.ProcessUserSID {
		return ErrConfig
	}
	if !reference(p.Provisioning.Reference) || p.Provisioning.ProtectedAt.IsZero() || !driveRoot(p.Volume.Root) || p.Volume.Serial == 0 || (p.Volume.Filesystem != "NTFS" && p.Volume.Filesystem != "ReFS") {
		return ErrConfig
	}
	for ref, key := range p.Catalog {
		if !reference(ref) || !fingerprint(key.Fingerprint) || !canonicalPath(key.Path) || !strings.HasPrefix(key.Path, p.Volume.Root) {
			return ErrConfig
		}
		parts := strings.Split(strings.TrimPrefix(key.Path, p.Volume.Root), `\`)
		if len(parts) < 1 || len(parts)+1 != len(key.Nodes) || len(key.Nodes) > 32 {
			return ErrConfig
		}
		path := p.Volume.Root
		for i, node := range key.Nodes {
			if i > 0 {
				if i > 1 {
					path += `\`
				}
				path += parts[i-1]
			}
			if node.Path != path || !trusted(p, node.OwnerSID) || node.ACL == nil || len(node.ACL) > 128 {
				return ErrConfig
			}
			if !safeACL(p, node.ACL, i == len(key.Nodes)-1) {
				return ErrConfig
			}
		}
	}
	return nil
}

func reference(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func fingerprint(s string) bool {
	if !strings.HasPrefix(s, "SHA256:") {
		return false
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(s, "SHA256:"))
	return err == nil && len(b) == 32 && "SHA256:"+base64.RawStdEncoding.EncodeToString(b) == s
}

func specificSID(s, prefix string, count int) bool {
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(s, prefix), "-")
	if len(parts) != count {
		return false
	}
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}

func processSID(s string) bool {
	return s == "S-1-5-18" || s == "S-1-5-19" || s == "S-1-5-20" || specificSID(s, "S-1-5-21-", 4)
}
func trusted(p Profile, s string) bool {
	return s != "" && s != p.GuestSID && (s == p.ProcessUserSID || s == p.ServiceSID || s == p.OperatorSID || s == "S-1-5-18" || s == "S-1-5-32-544")
}

func driveRoot(path string) bool {
	return len(path) == 3 && path[0] >= 'A' && path[0] <= 'Z' && path[1:] == `:\`
}
func canonicalPath(path string) bool {
	if len(path) <= 3 || len(path) > 1024 || !driveRoot(path[:3]) {
		return false
	}
	for _, part := range strings.Split(path[3:], `\`) {
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
