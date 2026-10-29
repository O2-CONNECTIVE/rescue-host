package guestaccount

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestFixedLayoutRejectsInvalidBindingsProfilesAndUnsupportedPolicy(t *testing.T) {
	data, _, _ := fixedLayoutFixture(t)
	cases := map[string]func(map[string]any){
		"protocol":           func(v map[string]any) { v["protocol"] = "rescue-guest-provisioning/2" },
		"zero-installation":  func(v map[string]any) { v["installation"] = strings.Repeat("0", 64) },
		"installation-upper": func(v map[string]any) { v["installation"] = strings.Repeat("A", 64) },
		"machine-account":    func(v map[string]any) { v["machineSID"] = "S-1-5-21-10-20-30-1000" },
		"service-user":       func(v map[string]any) { v["serviceUserSID"] = "S-1-1-0" },
		"service-sid":        func(v map[string]any) { v["serviceSID"] = "S-1-5-18" },
		"operator-sid":       func(v map[string]any) { v["operatorSID"] = "S-1-5-18" },
		"service-name":       func(v map[string]any) { v["serviceName"] = "" },
		"zero-sha": func(v map[string]any) {
			v["powershell"].(map[string]any)["image"].(map[string]any)["sha256"] = strings.Repeat("0", 64)
		},
		"image-upper-sha": func(v map[string]any) {
			v["powershell"].(map[string]any)["image"].(map[string]any)["sha256"] = strings.Repeat("B", 64)
		},
		"zero-fileid":         func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["fileID"] = strings.Repeat("0", 32) },
		"upper-fileid":        func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["fileID"] = strings.Repeat("C", 32) },
		"zero-volume":         func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["volume"] = "0" },
		"leading-zero-volume": func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["volume"] = "09" },
		"overflow-volume":     func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["volume"] = "18446744073709551616" },
		"owner-empty":         func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["ownerSID"] = "" },
		"descriptor-empty":    func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["securitySDDL"] = "" },
		"path-escape":         func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["path"] = `C:\Rescue\..` },
		"directory-role":      func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["directory"] = true },
		"journal-missing":     func(v map[string]any) { f := v["files"].([]any); v["files"] = append(f[:6], f[7:]...) },
		"ancestor-missing":    func(v map[string]any) { v["files"] = v["files"].([]any)[1:] },
		"artifact-missing":    func(v map[string]any) { v["files"] = v["files"].([]any)[:9] },
		"identity-collision": func(v map[string]any) {
			f := v["files"].([]any)
			f[5].(map[string]any)["fileID"] = f[4].(map[string]any)["fileID"]
		},
		"unsupported-group":    func(v map[string]any) { v["policy"].(map[string]any)["groups"] = []string{"S-1-5-32-544"} },
		"missing-seed":         func(v map[string]any) { p := v["policy"].(map[string]any); p["groups"] = p["groups"].([]any)[1:] },
		"wrong-domain-primary": func(v map[string]any) { v["policy"].(map[string]any)["groups"].([]any)[0] = "S-1-5-21-1-2-3-513" },
		"duplicate-group":      func(v map[string]any) { g := v["policy"].(map[string]any)["groups"].([]any); g[0] = g[1] },
		"admin-privilege":      func(v map[string]any) { v["policy"].(map[string]any)["privileges"] = []string{"SeDebugPrivilege"} },
		"unknown-privilege":    func(v map[string]any) { v["policy"].(map[string]any)["privileges"] = []string{"SeFuturePrivilege"} },
		"unknown-logon": func(v map[string]any) {
			v["policy"].(map[string]any)["logonRights"] = []string{"SeRemoteInteractiveLogonRight"}
		},
		"duplicate-logon": func(v map[string]any) {
			v["policy"].(map[string]any)["logonRights"] = []string{"SeNetworkLogonRight", "SeNetworkLogonRight"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value := fixedLayoutMutate(t, data, mutate)
			if f, e := DecodeFixedLayout(value); e == nil || f != nil {
				t.Fatal("unsupported fixed layout accepted", e)
			}
		})
	}
}
func TestFixedLayoutPreservesSyntaxWhitespaceAndPairedUnicodeInExactDigest(t *testing.T) {
	data, p, c := fixedLayoutFixture(t)
	data = bytes.Replace(data, []byte("RescueHost"), []byte(`Rescue\ud83d\ude00`), 1)
	data = append(append([]byte(" \n\t"), data...), []byte("\r\n ")...)
	f, e := DecodeFixedLayout(data)
	if e != nil || !f.MatchesService("Rescue😀", p.binding.service, c.serviceSID, p.binding.operator) {
		t.Fatal("well formed UTF16 pair rejected", e)
	}
}

func TestFixedLayoutExactByteLimitCanonicalUint64AndZeroValueLookups(t *testing.T) {
	data, p, c := fixedLayoutFixture(t)
	bounded := append(append([]byte{}, data...), bytes.Repeat([]byte(" "), 256*1024-len(data))...)
	f, e := DecodeFixedLayout(bounded)
	if e != nil || f.Digest() != sha256.Sum256(bounded) {
		t.Fatal("exact byte bound rejected or normalized", e)
	}
	if f, e := DecodeFixedLayout(append(bounded, ' ')); f != nil || e == nil {
		t.Fatal("one byte over limit accepted")
	}
	for _, value := range []string{"+9", "-9", "9.0", "9e0", " 9", "09", "", "18446744073709551616"} {
		value := fixedLayoutMutate(t, data, func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["volume"] = value })
		if f, e := DecodeFixedLayout(value); f != nil || e == nil {
			t.Fatal("noncanonical volume accepted", value)
		}
	}
	max := fixedLayoutMutate(t, data, func(v map[string]any) {
		for _, file := range v["files"].([]any) {
			file.(map[string]any)["volume"] = "18446744073709551615"
		}
	})
	if f, e := DecodeFixedLayout(max); f == nil || e != nil {
		t.Fatal("uint64 maximum lost", e)
	}
	for _, bad := range [][4]string{{"other", p.binding.service, c.serviceSID, p.binding.operator}, {c.serviceName, "S-1-5-19", c.serviceSID, p.binding.operator}, {c.serviceName, p.binding.service, "S-1-5-80-5-4-3-2-1", p.binding.operator}, {c.serviceName, p.binding.service, c.serviceSID, p.binding.machine + "-9999"}} {
		if f.MatchesService(bad[0], bad[1], bad[2], bad[3]) {
			t.Fatal("partial service tuple accepted")
		}
	}
	for _, empty := range []*FixedLayout{nil, {}} {
		if empty.MatchesService("", "", "", "") || empty.IncludesArtifact("") || empty.IncludesHostKey("") || empty.IncludesConfiguration("", [16]byte{}) || empty.Digest() != ([32]byte{}) {
			t.Fatal("zero layout became configured")
		}
	}
}

func TestFixedLayoutIncludesAdditionalFixedArtifactsWithoutInventingRuntimeInputs(t *testing.T) {
	data, _, _ := fixedLayoutFixture(t)
	data = fixedLayoutMutate(t, data, func(v map[string]any) {
		files := v["files"].([]any)
		extra := map[string]any{}
		for k, value := range files[7].(map[string]any) {
			extra[k] = value
		}
		extra["path"] = `C:\Rescue\Bin\rescue-service.exe`
		extra["fileID"] = "0b000000000000000000000000000000"
		v["files"] = append(files, extra)
	})
	f, e := DecodeFixedLayout(data)
	if e != nil || !f.IncludesArtifact(`c:\rescue\bin\RESCUE-service.exe`) {
		t.Fatal("additional fixed artifact omitted", e)
	}
	if f.profile.native.token != 0 || f.profile.native.stdio.UserSID != "" || f.profile.native.stdio.ServiceSID != "" || f.profile.native.stdio.CleanupTimeout != 0 {
		t.Fatal("decode invented runtime acquisition values")
	}
}

func TestFixedLayoutRejectsPowerShellPathsThatCannotUseFixedNativeFactory(t *testing.T) {
	data, _, _ := fixedLayoutFixture(t)
	for key, path := range map[string]string{"image": `C:\Rescue\Bin\powershell.txt`, "manifest": `C:\Rescue\Bin\other.psd1`, "assembly": `C:\Rescue\Microsoft.PowerShell.LocalAccounts.dll`} {
		t.Run(key, func(t *testing.T) {
			value := fixedLayoutMutate(t, data, func(v map[string]any) {
				a := v["powershell"].(map[string]any)[key].(map[string]any)
				old := a["path"].(string)
				a["path"] = path
				for _, file := range v["files"].([]any) {
					f := file.(map[string]any)
					if f["path"] == old {
						f["path"] = path
					}
				}
			})
			if f, e := DecodeFixedLayout(value); f != nil || e == nil {
				t.Fatal("unusable fixed native paths accepted", key)
			}
		})
	}
}
