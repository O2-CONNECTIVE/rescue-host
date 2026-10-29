package guestaccount

import (
	"strings"
	"unicode/utf8"
)

// 固定provisioningの期待値だけを扱う。実測値からprofileを生成しない。
type stagingFile struct {
	path      string
	role      permissionRole
	directory bool
	identity  journalIdentity
}
type stagingProfile struct {
	installation                         [32]byte
	serviceName, serviceSID, journalRoot string
	native                               sidNativeProfile
	files                                []stagingFile
}

func newStagingProfile(p Plan, c stagingProfile) (stagingProfile, error) {
	if !p.valid || p.binding.sid != "" || c.installation == ([32]byte{}) || !stagingServiceName(c.serviceName) || !strings.HasPrefix(c.serviceSID, "S-1-5-80-") || !serviceIdentity(c.serviceSID) || c.native.stdio.UserSID != p.binding.service || c.native.stdio.ServiceSID != c.serviceSID || !serviceIdentity(p.binding.service) || c.native.stdio.CleanupTimeout <= 0 || !realJournalHandle(c.native.token) || c.native.image.SHA256 == ([32]byte{}) || c.native.manifest.SHA256 == ([32]byte{}) || c.native.assembly.SHA256 == ([32]byte{}) {
		return stagingProfile{}, ErrUnconfigured
	}
	if !stagingFiles(c) {
		return stagingProfile{}, ErrUnconfigured
	}
	c.files = append([]stagingFile(nil), c.files...)
	return c, nil
}

func stagingServiceName(s string) bool {
	if s == "" || !utf8.ValidString(s) || len(s) > 4*256 || stagingUnits(s) > 256 || strings.TrimSpace(s) != s || strings.ContainsAny(s, `/\`) {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func stagingUnits(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func stagingFiles(c stagingProfile) bool {
	// service/process/token/既存control pipeの4対象を含め、inspector上限32以内。
	if len(c.files) == 0 || len(c.files) > 28 || !stagingPath(c.journalRoot, true) || !stagingPath(c.native.image.Path, false) || !stagingPath(c.native.manifest.Path, false) || !stagingPath(c.native.assembly.Path, false) {
		return false
	}
	counts := map[permissionRole]int{}
	required := []string{c.native.image.Path, c.native.manifest.Path, c.native.assembly.Path}
	for i, f := range c.files {
		if !stagingPath(f.path, f.directory) {
			return false
		}
		if !validJournalIdentity(f.identity) || len(f.identity.security) > 64*1024 || !utf8.ValidString(f.identity.security) || strings.ContainsAny(f.identity.security, "\x00\r\n") {
			return false
		}
		if _, e := permissionFactsSID(f.identity.owner); e != nil {
			return false
		}
		if f.role < roleConfiguration || f.role > roleAncestor || f.directory != (f.role == roleAncestor) {
			return false
		}
		counts[f.role]++
		for _, other := range c.files[:i] {
			if strings.EqualFold(f.path, other.path) || (f.identity.volume == other.identity.volume && f.identity.fileID == other.identity.fileID) {
				return false
			}
		}
	}
	if counts[roleJournal] != 1 || counts[roleConfiguration] < 1 || counts[roleHostKey] < 1 || counts[roleArtifact] != 3 {
		return false
	}
	for _, path := range required {
		f, ok := c.file(path)
		if !ok || f.role != roleArtifact {
			return false
		}
	}
	for i, path := range required {
		for _, other := range required[:i] {
			if strings.EqualFold(path, other) {
				return false
			}
		}
	}
	root, ok := c.file(c.journalRoot)
	if !ok || root.role != roleAncestor {
		return false
	}
	ledger := c.journalRoot + `\` + journalBasename
	if strings.HasSuffix(c.journalRoot, `\`) {
		ledger = c.journalRoot + journalBasename
	}
	j, ok := c.file(ledger)
	if !ok || j.role != roleJournal || j.identity.volume != root.identity.volume {
		return false
	}
	for _, f := range c.files {
		for parent := stagingParent(f.path); parent != ""; parent = stagingParent(parent) {
			ancestor, ok := c.file(parent)
			if !ok || ancestor.role != roleAncestor {
				return false
			}
		}
	}
	return true
}
func (c stagingProfile) file(path string) (stagingFile, bool) {
	for _, f := range c.files {
		if strings.EqualFold(path, f.path) {
			return f, true
		}
	}
	return stagingFile{}, false
}
func stagingParent(path string) string {
	if len(path) <= 3 {
		return ""
	}
	i := strings.LastIndex(path, `\`)
	if i == 2 {
		return path[:3]
	}
	if i < 2 {
		return ""
	}
	return path[:i]
}

// Windows のローカル絶対pathだけを字句検査する。OS上の正規化や保護の証明ではない。
// drive root はancestorとしてだけ許可し、pathを補完/正規化しない。
func stagingPath(path string, directory bool) bool {
	if len(path) < 3 || len(path) > 4*32766 || !utf8.ValidString(path) || stagingUnits(path) > 32766 || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1:3] != `:\` {
		return false
	}
	if len(path) == 3 {
		return directory
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if part == "" || part == "." || part == ".." || stagingUnits(part) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, `<>:"/|?*`) {
			return false
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return false
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		base = strings.TrimRight(base, " ")
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" {
			return false
		}
		if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
			tail := base[3:]
			if len(tail) == 1 && tail[0] >= '1' && tail[0] <= '9' || tail == "¹" || tail == "²" || tail == "³" {
				return false
			}
		}
	}
	return true
}
