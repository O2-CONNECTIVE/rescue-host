package guestaccount

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

func stagingProfileFixture(t *testing.T) (Plan, stagingProfile) {
	t.Helper()
	p := testPlan(t)
	c := stagingProfile{installation: [32]byte{1}, serviceName: "RescueHost", serviceSID: "S-1-5-80-1-2-3-4-5", journalRoot: `C:\Rescue\State`}
	c.native = sidNativeProfile{token: 11, stdio: childstdio.Setup{UserSID: p.binding.service, ServiceSID: c.serviceSID, CleanupTimeout: time.Second}, image: childjob.Artifact{Path: `C:\Rescue\Bin\powershell.exe`, SHA256: [32]byte{1}}, manifest: childjob.Artifact{Path: `C:\Rescue\Bin\Microsoft.PowerShell.LocalAccounts.psd1`, SHA256: [32]byte{2}}, assembly: childjob.Artifact{Path: `C:\Rescue\Bin\Microsoft.PowerShell.LocalAccounts.dll`, SHA256: [32]byte{3}}}
	for _, f := range []struct {
		path      string
		role      permissionRole
		directory bool
	}{
		{`C:\`, roleAncestor, true}, {`C:\Rescue`, roleAncestor, true}, {`C:\Rescue\State`, roleAncestor, true}, {`C:\Rescue\Bin`, roleAncestor, true},
		{`C:\Rescue\config.bin`, roleConfiguration, false}, {`C:\Rescue\host.key`, roleHostKey, false}, {`C:\Rescue\State\guest-journal-v1.bin`, roleJournal, false},
		{c.native.image.Path, roleArtifact, false}, {c.native.manifest.Path, roleArtifact, false}, {c.native.assembly.Path, roleArtifact, false},
	} {
		c.files = append(c.files, stagingFile{path: f.path, role: f.role, directory: f.directory, identity: journalIdentity{volume: 9, fileID: [16]byte{byte(len(c.files) + 1)}, owner: p.binding.service, security: "D:P(A;;FA;;;SY)"}})
	}
	return p, c
}

func TestStagingProfileAcceptsCompleteFixedManifestAndOwnsSnapshot(t *testing.T) {
	p, c := stagingProfileFixture(t)
	got, e := newStagingProfile(p, c)
	if e != nil || len(got.files) != 10 || got.installation != c.installation || got.native != c.native || got.serviceSID != c.serviceSID || got.journalRoot != c.journalRoot {
		t.Fatal("complete fixed profile rejected", got, e)
	}
	c.files[0].path = `D:\`
	c.files[1].identity.owner = "S-1-1-0"
	c.native.image.Path = `D:\other.exe`
	if got.files[0].path != `C:\` || got.files[1].identity.owner != p.binding.service || got.native.image.Path != `C:\Rescue\Bin\powershell.exe` {
		t.Fatal("caller mutation changed frozen profile")
	}
}

func TestStagingProfileRejectsUnboundOrIncompleteProtectedValues(t *testing.T) {
	cases := map[string]func(*Plan, *stagingProfile){
		"invalid-plan":              func(p *Plan, c *stagingProfile) { p.valid = false },
		"already-owned-sid":         func(p *Plan, c *stagingProfile) { p.binding.sid = p.binding.machine + "-2000" },
		"installation":              func(p *Plan, c *stagingProfile) { c.installation = [32]byte{} },
		"service-name":              func(p *Plan, c *stagingProfile) { c.serviceName = "" },
		"service-name-path":         func(p *Plan, c *stagingProfile) { c.serviceName = `Service\Other` },
		"service-name-invalid-utf8": func(p *Plan, c *stagingProfile) { c.serviceName = "bad\xff" },
		"service-sid-account": func(p *Plan, c *stagingProfile) {
			c.serviceSID = p.binding.operator
			c.native.stdio.ServiceSID = c.serviceSID
		},
		"service-sid-noncanonical": func(p *Plan, c *stagingProfile) {
			c.serviceSID = "S-1-5-80-01-2-3-4-5"
			c.native.stdio.ServiceSID = c.serviceSID
		},
		"pipe-user":    func(p *Plan, c *stagingProfile) { c.native.stdio.UserSID = p.binding.operator },
		"pipe-service": func(p *Plan, c *stagingProfile) { c.native.stdio.ServiceSID = "S-1-5-80-5-4-3-2-1" },
		"cleanup":      func(p *Plan, c *stagingProfile) { c.native.stdio.CleanupTimeout = 0 },
		"token-zero":   func(p *Plan, c *stagingProfile) { c.native.token = 0 },
		"token-pseudo": func(p *Plan, c *stagingProfile) { c.native.token = ^uintptr(0) },
		"image-sha":    func(p *Plan, c *stagingProfile) { c.native.image.SHA256 = [32]byte{} },
		"manifest-sha": func(p *Plan, c *stagingProfile) { c.native.manifest.SHA256 = [32]byte{} },
		"assembly-sha": func(p *Plan, c *stagingProfile) { c.native.assembly.SHA256 = [32]byte{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, c := stagingProfileFixture(t)
			mutate(&p, &c)
			got, e := newStagingProfile(p, c)
			if e == nil || len(got.files) != 0 || got.installation != ([32]byte{}) {
				t.Fatal("unbound profile accepted", e)
			}
		})
	}
}

func TestStagingProfileRejectsMissingRolesCollisionsAndUnpinnedIdentity(t *testing.T) {
	cases := map[string]func(*stagingProfile){
		"files-empty": func(c *stagingProfile) { c.files = nil },
		"files-over-bound": func(c *stagingProfile) {
			for len(c.files) < 30 {
				c.files = append(c.files, c.files[4])
			}
		},
		"unknown-role":          func(c *stagingProfile) { c.files[4].role = 99 },
		"service-is-not-file":   func(c *stagingProfile) { c.files[4].role = roleService },
		"pipe-is-not-file":      func(c *stagingProfile) { c.files[4].role = roleControlPipe },
		"configuration-missing": func(c *stagingProfile) { c.files = append(c.files[:4], c.files[5:]...) },
		"key-missing":           func(c *stagingProfile) { c.files = append(c.files[:5], c.files[6:]...) },
		"journal-missing":       func(c *stagingProfile) { c.files = append(c.files[:6], c.files[7:]...) },
		"journal-extra": func(c *stagingProfile) {
			f := c.files[6]
			f.path = `C:\Rescue\State\other.bin`
			f.identity.fileID[0] = 33
			c.files = append(c.files, f)
		},
		"journal-wrong-location":        func(c *stagingProfile) { c.files[6].path = `C:\Rescue\guest-journal-v1.bin` },
		"journal-root-wrong-role":       func(c *stagingProfile) { c.files[2].role = roleConfiguration; c.files[2].directory = false },
		"root-file":                     func(c *stagingProfile) { c.files[0].directory = false },
		"artifact-dir":                  func(c *stagingProfile) { c.files[7].directory = true },
		"configuration-dir":             func(c *stagingProfile) { c.files[4].directory = true },
		"path-case-alias":               func(c *stagingProfile) { c.files[5].path = `c:\rescue\CONFIG.bin` },
		"identity-alias":                func(c *stagingProfile) { c.files[5].identity = c.files[4].identity },
		"volume-zero":                   func(c *stagingProfile) { c.files[4].identity.volume = 0 },
		"fileid-zero":                   func(c *stagingProfile) { c.files[4].identity.fileID = [16]byte{} },
		"owner-empty":                   func(c *stagingProfile) { c.files[4].identity.owner = "" },
		"security-empty":                func(c *stagingProfile) { c.files[4].identity.security = "" },
		"owner-noncanonical":            func(c *stagingProfile) { c.files[4].identity.owner = "S-1-5-018" },
		"security-invalid-utf8":         func(c *stagingProfile) { c.files[4].identity.security = "D:P\xff" },
		"artifact-missing":              func(c *stagingProfile) { c.files = c.files[:9] },
		"artifact-path-mismatch":        func(c *stagingProfile) { c.files[7].path = `C:\Rescue\Bin\other.exe` },
		"artifact-same-path":            func(c *stagingProfile) { c.native.manifest.Path = c.native.image.Path },
		"ancestor-drive-missing":        func(c *stagingProfile) { c.files = c.files[1:] },
		"ancestor-intermediate-missing": func(c *stagingProfile) { c.files = append(c.files[:1], c.files[2:]...) },
		"artifact-parent-missing":       func(c *stagingProfile) { c.files = append(c.files[:3], c.files[4:]...) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, c := stagingProfileFixture(t)
			mutate(&c)
			if _, e := newStagingProfile(p, c); e == nil {
				t.Fatal("incomplete or colliding manifest accepted")
			}
		})
	}
}

func TestStagingProfileHoldsServiceArtifactsAlongsidePowerShell(t *testing.T) {
	p, c := stagingProfileFixture(t)
	for i, path := range []string{`C:\Rescue\Bin\supervisor.exe`, `C:\Rescue\Bin\backend.exe`} {
		f := c.files[7]
		f.path = path
		f.identity.fileID = [16]byte{byte(33 + i)}
		c.files = append(c.files, f)
	}
	if _, e := newStagingProfile(p, c); e != nil {
		t.Fatal("保護recordが指定するService/Go imageを保持できない", e)
	}
	c.files[7].path = `C:\Rescue\Bin\missing-powershell.exe`
	if _, e := newStagingProfile(p, c); e == nil {
		t.Fatal("追加imageで必須PowerShellを代用")
	}
}

func TestStagingProfileRejectsNoncanonicalWindowsPaths(t *testing.T) {
	for name, path := range map[string]string{
		"unc": `\\server\share\config.bin`, "device": `\\?\C:\Rescue\config.bin`, "relative": `config.bin`, "drive-relative": `C:config.bin`, "slash": `C:/Rescue/config.bin`,
		"dot": `C:\Rescue\.`, "dotdot": `C:\Rescue\..`, "trailing-dot": `C:\Rescue\config.bin.`, "trailing-space": `C:\Rescue\config.bin `, "trailing-slash": `C:\Rescue\`,
		"ads": `C:\Rescue\config.bin:stream`, "wildcard": `C:\Rescue\config?.bin`, "control": "C:\\Rescue\\bad\x01.bin", "invalid-utf8": "C:\\Rescue\\bad\xff.bin",
		"nul-device": `C:\Rescue\NUL`, "com-device": `C:\Rescue\COM1.bin`,
	} {
		t.Run(name, func(t *testing.T) {
			p, c := stagingProfileFixture(t)
			c.files[4].path = path
			if _, e := newStagingProfile(p, c); e == nil {
				t.Fatal("noncanonical path accepted", path)
			}
		})
	}
	t.Run("artifact-path", func(t *testing.T) {
		p, c := stagingProfileFixture(t)
		c.native.image.Path = `C:\Rescue\Bin\powershell.exe.`
		c.files[7].path = c.native.image.Path
		if _, e := newStagingProfile(p, c); e == nil {
			t.Fatal("noncanonical artifact accepted")
		}
	})
	t.Run("journal-root-path", func(t *testing.T) {
		p, c := stagingProfileFixture(t)
		c.journalRoot = `C:\Rescue\State.`
		c.files[2].path = c.journalRoot
		c.files[6].path = c.journalRoot + `\guest-journal-v1.bin`
		if _, e := newStagingProfile(p, c); e == nil {
			t.Fatal("noncanonical journal root accepted")
		}
	})
}

func TestStagingProfileAllowsDriveRootJournalAndCaseInsensitiveReferences(t *testing.T) {
	p, c := stagingProfileFixture(t)
	c.journalRoot = `c:\`
	c.files[6].path = `C:\guest-journal-v1.bin`
	c.native.image.Path = `c:\RESCUE\bin\powershell.exe`
	if _, e := newStagingProfile(p, c); e != nil {
		t.Fatal("drive root or canonical case variants rejected", e)
	}
}

func TestStagingProfileBoundsWholeManifestAndNativeNames(t *testing.T) {
	p, c := stagingProfileFixture(t)
	for len(c.files) < 28 {
		f := c.files[4]
		f.path = fmt.Sprintf(`C:\Rescue\config-%d.bin`, len(c.files))
		f.identity.fileID = [16]byte{byte(len(c.files) + 1)}
		c.files = append(c.files, f)
	}
	c.serviceName = strings.Repeat("s", 256)
	if _, e := newStagingProfile(p, c); e != nil {
		t.Fatal("bounded complete manifest rejected", e)
	}
	f := c.files[4]
	f.path = `C:\Rescue\config-29.bin`
	f.identity.fileID = [16]byte{31}
	c.files = append(c.files, f)
	if _, e := newStagingProfile(p, c); e == nil {
		t.Fatal("29 unique file entries accepted")
	}
	_, c = stagingProfileFixture(t)
	c.serviceName = strings.Repeat("s", 257)
	if _, e := newStagingProfile(p, c); e == nil {
		t.Fatal("oversize service name accepted")
	}
	_, c = stagingProfileFixture(t)
	c.files[4].path = `C:\Rescue\` + strings.Repeat("x", 251) + ".bin"
	if _, e := newStagingProfile(p, c); e != nil {
		t.Fatal("bounded filename rejected", e)
	}
	c.files[4].path = `C:\Rescue\` + strings.Repeat("x", 252) + ".bin"
	if _, e := newStagingProfile(p, c); e == nil {
		t.Fatal("oversize filename accepted")
	}
	_, c = stagingProfileFixture(t)
	c.files[4].identity.security = strings.Repeat("x", 64*1024+1)
	if _, e := newStagingProfile(p, c); e == nil {
		t.Fatal("oversize expected descriptor accepted")
	}
}

func TestStagingProfileAllowsOrdinaryDevicePrefixButRejectsReservedBasenames(t *testing.T) {
	p, c := stagingProfileFixture(t)
	c.files[4].path = `C:\Rescue\COMLPT1.bin`
	if _, e := newStagingProfile(p, c); e != nil {
		t.Fatal("ordinary filename with device-like prefix rejected", e)
	}
	for _, name := range []string{"con.txt", "AUX", "PRN.bin", "NUL .bin", "LPT9.cfg", "COM².bin", "LPT³", "CONIN$", "CONOUT$"} {
		_, c = stagingProfileFixture(t)
		c.files[4].path = `C:\Rescue\` + name
		if _, e := newStagingProfile(p, c); e == nil {
			t.Fatal("reserved basename accepted", name)
		}
	}
}
