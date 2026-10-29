package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func guestConfigurationFixture(t *testing.T) (*Prepared, *Root, *rootFakeKernel, []byte) {
	t.Helper()
	p, _, _ := previewPreparedFixture(t)
	base, e := p.loaded.Record()
	if e != nil {
		t.Fatal(e)
	}
	p.record, e = DecodeRecord(base)
	if e != nil {
		t.Fatal(e)
	}
	a := p.loaded.anchor
	a.RecordPath = `C:\safe\guest-provisioning-v1.json`
	a.DigestPath = `C:\safe\guest-provisioning-v1.sha256`
	k := rootFixture(a)
	k.files[a.RecordPath].state.id = [16]byte{5}
	k.files[a.DigestPath].state.id = [16]byte{6}
	machine := "S-1-5-21-1-2-3"
	files := []map[string]any{}
	for i, v := range []struct {
		path, role string
		directory  bool
	}{
		{`C:\`, "ancestor", true}, {`C:\safe`, "ancestor", true}, {p.loaded.anchor.RecordPath, "configuration", false}, {p.loaded.anchor.DigestPath, "configuration", false}, {a.RecordPath, "configuration", false}, {a.DigestPath, "configuration", false},
		{p.record.Profile.SupervisorImage.Path, "artifact", false}, {p.record.Profile.ChildImage.Path, "artifact", false}, {`C:\host-key`, "hostKey", false}, {`C:\safe\guest-journal-v1.bin`, "journal", false},
		{`C:\powershell.exe`, "artifact", false}, {`C:\Microsoft.PowerShell.LocalAccounts.psd1`, "artifact", false}, {`C:\Microsoft.PowerShell.LocalAccounts.dll`, "artifact", false},
	} {
		id := [16]byte{byte(i + 1)}
		files = append(files, map[string]any{"path": v.path, "role": v.role, "directory": v.directory, "volume": "7", "fileID": hex.EncodeToString(id[:]), "ownerSID": "S-1-5-18", "securitySDDL": "O:SYD:P(A;;FA;;;SY)"})
	}
	artifact := func(path string) map[string]string {
		return map[string]string{"path": path, "sha256": strings.Repeat("01", 32)}
	}
	payload := map[string]any{"protocol": "rescue-guest-provisioning/1", "installation": strings.Repeat("01", 32), "machineSID": machine, "serviceUserSID": p.record.Profile.Keys.ProcessUserSID, "serviceSID": p.record.Profile.Keys.ServiceSID, "operatorSID": p.record.Profile.Keys.OperatorSID, "serviceName": p.record.Profile.ServiceName, "journalRoot": `C:\safe`, "powershell": map[string]any{"image": artifact(`C:\powershell.exe`), "manifest": artifact(`C:\Microsoft.PowerShell.LocalAccounts.psd1`), "assembly": artifact(`C:\Microsoft.PowerShell.LocalAccounts.dll`)}, "files": files, "policy": map[string]any{"groups": []string{machine + "-513", "S-1-1-0", "S-1-2-0", "S-1-5-11", "S-1-5-113", "S-1-5-15", "S-1-5-2", "S-1-5-32-545"}, "privileges": []string{"SeChangeNotifyPrivilege"}, "logonRights": []string{"SeNetworkLogonRight"}}}
	data, e := json.Marshal(payload)
	if e != nil {
		t.Fatal(e)
	}
	setGuestFixtureData(k, a, data)
	root, e := NewNativeRoot(a)
	if e != nil {
		t.Fatal(e)
	}
	root.api = k
	return p, root, k, data
}
func setGuestFixtureData(k *rootFakeKernel, a Anchor, data []byte) {
	k.files[a.RecordPath].data = data
	k.files[a.RecordPath].state.size = int64(len(data))
	hash := sha256.Sum256(data)
	k.files[a.DigestPath].data = []byte(hex.EncodeToString(hash[:]) + "\n")
	k.files[a.DigestPath].state.size = 65
}

func TestFixedGuestConfigurationUsesProtectedPairAndExactServiceTargets(t *testing.T) {
	p, root, k, data := guestConfigurationFixture(t)
	g, e := p.loadFixedGuest(rootContext(t), root)
	if e != nil || g == nil {
		t.Fatal("保護sourceからguest配置へ到達しない", e)
	}
	layout, e := g.Layout(rootContext(t))
	if e != nil || layout.Digest() != sha256.Sum256(data) {
		t.Fatal("同じ検証済みbytesでない", e)
	}
	if len(k.opened) != 4 || g.loaded == nil {
		t.Fatal("既存Rootの祖先/record/digest保持を省略")
	}
	if e = g.Close(rootContext(t)); e != nil {
		t.Fatal(e)
	}
	if _, e = g.Layout(rootContext(t)); e == nil {
		t.Fatal("Close後の配置証拠復活")
	}
}
func TestFixedGuestConfigurationRejectsDifferentRootSubjectOrProtectedRole(t *testing.T) {
	for _, kind := range []string{"root", "subject", "host-key", "service-image", "base-file-id", "digest"} {
		t.Run(kind, func(t *testing.T) {
			p, root, k, data := guestConfigurationFixture(t)
			switch kind {
			case "root":
				root.anchor.RecordPath = `C:\safe\other.json`
			case "subject":
				data = []byte(strings.ReplaceAll(string(data), p.record.Profile.Keys.OperatorSID, "S-1-5-21-1-2-3-9001"))
				setGuestFixtureData(k, root.anchor, data)
			case "host-key":
				data = []byte(strings.ReplaceAll(string(data), `"role":"hostKey"`, `"role":"configuration"`))
				setGuestFixtureData(k, root.anchor, data)
			case "service-image":
				data = []byte(strings.ReplaceAll(string(data), `C:\\child.exe`, `C:\\other.exe`))
				setGuestFixtureData(k, root.anchor, data)
			case "base-file-id":
				data = []byte(strings.ReplaceAll(string(data), `03000000000000000000000000000000`, `33000000000000000000000000000000`))
				setGuestFixtureData(k, root.anchor, data)
			case "digest":
				k.files[root.anchor.DigestPath].data = []byte(fmt.Sprintf("%064d\n", 0))
			}
			g, e := p.loadFixedGuest(rootContext(t), root)
			if e == nil {
				t.Fatal("別source/roleを採用")
			}
			if g != nil {
				_ = g.Close(rootContext(t))
			}
		})
	}
}
func TestFixedGuestConfigurationRejectsCanceledBeforeOpeningGuestFiles(t *testing.T) {
	p, root, k, _ := guestConfigurationFixture(t)
	ctx, cancel := context.WithCancel(rootContext(t))
	cancel()
	if g, e := p.loadFixedGuest(ctx, root); e == nil || g != nil || len(k.opened) != 0 {
		t.Fatal("取消後の取得", g, e)
	}
}

func TestFixedGuestConfigurationRechecksHeldRootBeforeReturningLayout(t *testing.T) {
	p, root, k, _ := guestConfigurationFixture(t)
	g, e := p.loadFixedGuest(rootContext(t), root)
	if e != nil {
		t.Fatal(e)
	}
	k.files[root.anchor.RecordPath].state.id = [16]byte{99}
	if _, e = g.Layout(rootContext(t)); e == nil {
		t.Fatal("保持fileの同一性変化を再照合せず")
	}
	k.files[root.anchor.RecordPath].state.id = [16]byte{5}
	if _, e = g.Layout(rootContext(t)); e == nil {
		t.Fatal("失効したsourceを復活")
	}
}
