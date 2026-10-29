package guestaccount

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func fixedLayoutFixture(t *testing.T) ([]byte, Plan, stagingProfile) {
	t.Helper()
	p, c := stagingProfileFixture(t)
	artifact := func(path string, hash [32]byte) any {
		return map[string]any{"path": path, "sha256": hex.EncodeToString(hash[:])}
	}
	files := []any{}
	roles := map[permissionRole]string{roleConfiguration: "configuration", roleJournal: "journal", roleHostKey: "hostKey", roleArtifact: "artifact", roleAncestor: "ancestor"}
	for _, f := range c.files {
		files = append(files, map[string]any{"path": f.path, "role": roles[f.role], "directory": f.directory, "volume": "9", "fileID": hex.EncodeToString(f.identity.fileID[:]), "ownerSID": f.identity.owner, "securitySDDL": f.identity.security})
	}
	v := map[string]any{"protocol": "rescue-guest-provisioning/1", "installation": hex.EncodeToString(c.installation[:]), "machineSID": p.binding.machine, "serviceUserSID": p.binding.service, "serviceSID": c.serviceSID, "operatorSID": p.binding.operator, "serviceName": c.serviceName, "journalRoot": c.journalRoot, "powershell": map[string]any{"image": artifact(c.native.image.Path, c.native.image.SHA256), "manifest": artifact(c.native.manifest.Path, c.native.manifest.SHA256), "assembly": artifact(c.native.assembly.Path, c.native.assembly.SHA256)}, "files": files, "policy": map[string]any{"groups": []string{p.binding.machine + "-513", "S-1-1-0", "S-1-2-0", "S-1-5-11", "S-1-5-113", "S-1-5-15", "S-1-5-2", "S-1-5-32-545"}, "privileges": []string{"SeChangeNotifyPrivilege"}, "logonRights": []string{"SeNetworkLogonRight", "SeDenyRemoteInteractiveLogonRight"}}}
	data, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return data, p, c
}
func TestFixedLayoutDecodesFixedManifestAndExposesOnlyBoundLookups(t *testing.T) {
	data, p, c := fixedLayoutFixture(t)
	f, e := DecodeFixedLayout(data)
	if e != nil || f == nil {
		t.Fatal("complete fixed layout rejected", e)
	}
	if !f.MatchesService(c.serviceName, p.binding.service, c.serviceSID, p.binding.operator) || f.MatchesService("other", p.binding.service, c.serviceSID, p.binding.operator) {
		t.Fatal("service tuple not exactly matched")
	}
	if !f.IncludesConfiguration(`c:\rescue\CONFIG.bin`, c.files[4].identity.volume, c.files[4].identity.fileID) || f.IncludesConfiguration(c.files[4].path, c.files[4].identity.volume, [16]byte{99}) || f.IncludesConfiguration(c.files[5].path, c.files[5].identity.volume, c.files[5].identity.fileID) {
		t.Fatal("configuration path/identity/role not bound")
	}
	if !f.IncludesArtifact(c.native.image.Path) || f.IncludesArtifact(c.files[4].path) || !f.IncludesHostKey(c.files[5].path) || f.IncludesHostKey(c.files[4].path) {
		t.Fatal("artifact/key role mismatch")
	}
	want := sha256.Sum256(data)
	if f.Digest() != want {
		t.Fatal("digest is not exact original bytes")
	}
	for i := range data {
		data[i] = 0
	}
	if f.Digest() != want || !f.IncludesArtifact(c.native.image.Path) {
		t.Fatal("caller bytes changed owned layout")
	}
}

func fixedLayoutMutate(t *testing.T, data []byte, change func(map[string]any)) []byte {
	t.Helper()
	var v map[string]any
	if json.Unmarshal(data, &v) != nil {
		t.Fatal("bad fixture")
	}
	change(v)
	out, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestFixedLayoutRejectsAmbiguousOrNonconformingJSON(t *testing.T) {
	data, _, _ := fixedLayoutFixture(t)
	cases := map[string][]byte{
		"root-unknown":          fixedLayoutMutate(t, data, func(v map[string]any) { v["token"] = "11" }),
		"root-case":             bytes.Replace(data, []byte(`"machineSID":`), []byte(`"MachineSID":`), 1),
		"missing-root":          fixedLayoutMutate(t, data, func(v map[string]any) { delete(v, "protocol") }),
		"unknown-file":          fixedLayoutMutate(t, data, func(v map[string]any) { v["files"].([]any)[0].(map[string]any)["extra"] = false }),
		"missing-false-bool":    fixedLayoutMutate(t, data, func(v map[string]any) { delete(v["files"].([]any)[4].(map[string]any), "directory") }),
		"null-array":            fixedLayoutMutate(t, data, func(v map[string]any) { v["policy"].(map[string]any)["privileges"] = nil }),
		"null-bool":             fixedLayoutMutate(t, data, func(v map[string]any) { v["files"].([]any)[4].(map[string]any)["directory"] = nil }),
		"unknown-artifact":      fixedLayoutMutate(t, data, func(v map[string]any) { v["powershell"].(map[string]any)["image"].(map[string]any)["size"] = "123" }),
		"unknown-policy":        fixedLayoutMutate(t, data, func(v map[string]any) { v["policy"].(map[string]any)["administrator"] = false }),
		"duplicate-root":        append([]byte(`{"protocol":"rescue-guest-provisioning/1",`), data[1:]...),
		"duplicate-escaped-key": append([]byte(`{"pro\u0074ocol":"rescue-guest-provisioning/1",`), data[1:]...),
		"duplicate-file":        bytes.Replace(data, []byte(`"directory":true`), []byte(`"directory":true,"directory":true`), 1),
		"volume-number":         fixedLayoutMutate(t, data, func(v map[string]any) { v["files"].([]any)[0].(map[string]any)["volume"] = 9 }),
		"top-array":             append(append([]byte("["), data...), ']'),
		"extra-object":          append(append([]byte{}, data...), []byte(` {}`)...),
		"partial":               append([]byte{}, data[:len(data)-1]...),
		"invalid-utf8":          bytes.Replace(data, []byte("RescueHost"), []byte{'b', 0xff}, 1),
		"lone-high-surrogate":   bytes.Replace(data, []byte("RescueHost"), []byte(`Rescue\ud800`), 1),
		"lone-low-surrogate":    bytes.Replace(data, []byte("RescueHost"), []byte(`Rescue\udc00`), 1),
		"surrogate-mismatch":    bytes.Replace(data, []byte("RescueHost"), []byte(`Rescue\ud800\u0041`), 1),
		"over-depth":            append([]byte(`{"x":`+strings.Repeat("[", 17)+"0"+strings.Repeat("]", 17)+","), data[1:]...),
		"over-bytes":            append(append([]byte{}, data...), bytes.Repeat([]byte(" "), 256*1024)...),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if f, e := DecodeFixedLayout(value); e == nil || f != nil {
				t.Fatal("ambiguous JSON accepted", e)
			}
		})
	}
}
