package bootstrapauth

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

func TestFactoryRejectsUnconfiguredProfile(t *testing.T) {
	if got, err := NewNativeFactory(Profile{}); got != nil || err != ErrConfig {
		t.Fatal("未構成の認証 factory を受理しました")
	}
}

func TestFactoryRejectsUnsafeImageAndIncompleteService(t *testing.T) {
	for name, mutate := range map[string]func(*Profile){
		"service":      func(p *Profile) { p.ServiceName = "" },
		"service path": func(p *Profile) { p.ServiceName = "x/y" },
		"budget":       func(p *Profile) { p.ChildImage.MaxBytes = 0 },
		"digest":       func(p *Profile) { p.SupervisorImage.SHA256 = [32]byte{} },
		"ancestor":     func(p *Profile) { p.ChildImage.Nodes = p.ChildImage.Nodes[1:] },
		"write": func(p *Profile) {
			p.ChildImage.Nodes[0].ACL = append(p.ChildImage.Nodes[0].ACL, protectedkey.ACE{SID: guest, Mask: 2})
		},
		"null acl": func(p *Profile) { p.ChildImage.Nodes[1].ACL = nil },
		"path":     func(p *Profile) { p.ChildImage.Path = `C:\safe\..\child.exe` },
		"owner":    func(p *Profile) { p.ChildImage.Nodes[0].OwnerSID = guest },
	} {
		t.Run(name, func(t *testing.T) {
			p := testProfile()
			mutate(&p)
			if f, e := NewNativeFactory(p); f != nil || e != ErrConfig {
				t.Fatal("危険または不足する固定入力を受理しました")
			}
		})
	}
	p := testProfile()
	p.ChildImage.Nodes[1].ACL = append(p.ChildImage.Nodes[1].ACL, protectedkey.ACE{SID: guest, Mask: 0xa0000000})
	if f, e := NewNativeFactory(p); f == nil || e != nil {
		t.Fatal("公開 exe の読取・実行だけを拒否しました")
	}
}

const service = "S-1-5-80-1-2-3-4-5"
const user = "S-1-5-21-1-2-3-1000"
const operator = "S-1-5-21-1-2-3-1001"
const guest = "S-1-5-21-1-2-3-1002"

func testProfile() Profile {
	volume := protectedkey.VolumeProfile{Root: `C:\`, Serial: 7, Filesystem: "NTFS"}
	nodes := func(path string) []protectedkey.NodePolicy {
		return []protectedkey.NodePolicy{
			{Path: `C:\`, OwnerSID: operator, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}},
			{Path: path, OwnerSID: operator, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}},
		}
	}
	keys := protectedkey.Profile{Version: 1, ProcessUserSID: user, ServiceSID: service, OperatorSID: operator, GuestSID: guest, Volume: volume, Provisioning: protectedkey.ProvisioningRecord{Reference: "synthetic", ProtectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Catalog: map[string]protectedkey.KeyProfile{"fixed": {Path: `C:\key`, Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Nodes: nodes(`C:\key`)}}}
	image := func(path string) Image {
		return Image{Path: path, SHA256: sha256.Sum256([]byte("exe")), MaxBytes: 1024, Volume: volume, Nodes: nodes(path)}
	}
	return Profile{Keys: keys, ServiceName: "SyntheticService", SupervisorImage: image(`C:\supervisor.exe`), ChildImage: image(`C:\child.exe`)}
}

func TestZeroFactoryCannotQueryNativeAuthentication(t *testing.T) {
	var f Factory
	if peer, e := f.Authenticate(context.Background(), 99); peer != nil || e != ErrConfig {
		t.Fatal("zero factory が認証境界へ進みました")
	}
	if proof, e := f.VerifyService(context.Background(), 73); proof != nil || e != ErrConfig {
		t.Fatal("zero factory が service 照会へ進みました")
	}
}
