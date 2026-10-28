package protectedkey

import (
	"testing"
	"time"
)

const (
	testOperator = "S-1-5-21-1-2-3-1001"
	testGuest    = "S-1-5-21-1-2-3-1002"
	testService  = "S-1-5-80-1-2-3-4-5"
)

func provisionedProfile() Profile {
	return Profile{
		Version: 1, ProcessUserSID: "S-1-5-18", ServiceSID: testService, OperatorSID: testOperator, GuestSID: testGuest,
		Volume:       VolumeProfile{Root: `C:\`, Serial: 7, Filesystem: "NTFS"},
		Provisioning: ProvisioningRecord{Reference: "synthetic-provisioning-record", ProtectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Catalog: map[string]KeyProfile{testRef: {
			Path: `C:\protected\host-key`, Fingerprint: testFingerprint,
			Nodes: []NodePolicy{
				{Path: `C:\`, OwnerSID: testOperator, ACL: []ACE{{Type: 0, SID: testService, Mask: 0x001f01ff}}},
				{Path: `C:\protected`, OwnerSID: testOperator, ACL: []ACE{{Type: 0, SID: testService, Mask: 0x001f01ff}}},
				{Path: `C:\protected\host-key`, OwnerSID: testOperator, ACL: []ACE{{Type: 0, SID: testService, Mask: 0x001f01ff}}},
			},
		}},
	}
}

func TestNativeRejectsMissingProvisioningProfile(t *testing.T) {
	loader, err := NewNative(Profile{})
	if loader != nil || err != ErrConfig {
		t.Fatal("missing provisioning accepted")
	}
}

func TestNativeAcceptsExplicitServiceAccountSID(t *testing.T) {
	for _, sid := range []string{"S-1-5-19", "S-1-5-20"} {
		profile := provisionedProfile()
		profile.ProcessUserSID = sid
		if loader, err := NewNative(profile); loader == nil || err != nil {
			t.Fatalf("explicit configured service account rejected: %v", err)
		}
	}
}

func TestNativeHasNoCallerExecutionContext(t *testing.T) {
	loader, err := NewNative(provisionedProfile())
	if err != nil || loader == nil {
		t.Fatal("valid fixed profile rejected")
	}
	signer, err := loader.Load(loadContext(t), testRef)
	if signer != nil || err != ErrClosed {
		t.Fatal("native path enabled without caller execution context")
	}
}

func TestNativeRejectsIncompleteOrAmbiguousProfile(t *testing.T) {
	mutations := map[string]func(*Profile){
		"version":             func(p *Profile) { p.Version = 2 },
		"process user":        func(p *Profile) { p.ProcessUserSID = "" },
		"service group":       func(p *Profile) { p.ServiceSID = "" },
		"guest":               func(p *Profile) { p.GuestSID = "" },
		"overlap":             func(p *Profile) { p.GuestSID = p.OperatorSID },
		"record":              func(p *Profile) { p.Provisioning.Reference = "" },
		"protected time":      func(p *Profile) { p.Provisioning.ProtectedAt = time.Time{} },
		"volume":              func(p *Profile) { p.Volume.Serial = 0 },
		"filesystem":          func(p *Profile) { p.Volume.Filesystem = "unknown" },
		"relative root":       func(p *Profile) { p.Volume.Root = `C:` },
		"unknown reference":   func(p *Profile) { p.Catalog["../host-key"] = p.Catalog[testRef] },
		"missing fingerprint": func(p *Profile) { k := p.Catalog[testRef]; k.Fingerprint = ""; p.Catalog[testRef] = k },
		"missing nodes":       func(p *Profile) { k := p.Catalog[testRef]; k.Nodes = nil; p.Catalog[testRef] = k },
		"missing ancestor": func(p *Profile) {
			k := p.Catalog[testRef]
			k.Nodes = append(k.Nodes[:1], k.Nodes[2])
			p.Catalog[testRef] = k
		},
		"missing ACL":   func(p *Profile) { k := p.Catalog[testRef]; k.Nodes[1].ACL = nil; p.Catalog[testRef] = k },
		"unknown owner": func(p *Profile) { k := p.Catalog[testRef]; k.Nodes[1].OwnerSID = testGuest; p.Catalog[testRef] = k },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := provisionedProfile()
			mutate(&p)
			if loader, err := NewNative(p); loader != nil || err != ErrConfig {
				t.Fatal("incomplete profile accepted")
			}
		})
	}
}

func TestNativeRejectsUnsafeExpectedACL(t *testing.T) {
	for name, ace := range map[string]ACE{
		"directory create file":           {SID: testGuest, Mask: 2},
		"directory create subdirectory":   {SID: testGuest, Mask: 4},
		"write EA":                        {SID: testGuest, Mask: 0x10},
		"write attributes":                {SID: testGuest, Mask: 0x100},
		"delete child":                    {SID: testGuest, Mask: 0x40},
		"delete":                          {SID: testGuest, Mask: 0x10000},
		"write DAC":                       {SID: testGuest, Mask: 0x40000},
		"write owner":                     {SID: testGuest, Mask: 0x80000},
		"generic write":                   {SID: testGuest, Mask: 0x40000000},
		"generic all":                     {SID: testGuest, Mask: 0x10000000},
		"unknown trustee dangerous allow": {SID: "S-1-1-0", Mask: 2},
		"unknown ACE":                     {Type: 5, SID: testService, Mask: 1},
		"unknown mask":                    {SID: testService, Mask: 0x02000000},
		"unknown flags":                   {Flags: 0x20, SID: testService, Mask: 1},
		"invalid SID":                     {SID: "not-a-SID", Mask: 1},
	} {
		t.Run(name, func(t *testing.T) {
			p := provisionedProfile()
			k := p.Catalog[testRef]
			k.Nodes[0].ACL = append([]ACE{{Type: 1, SID: testGuest, Mask: 0x001f01ff}}, ace)
			p.Catalog[testRef] = k
			if loader, err := NewNative(p); loader != nil || err != ErrConfig {
				t.Fatal("unsafe expected ACL accepted")
			}
		})
	}
	for _, mask := range []uint32{1, 0x20, 0x80000000, 0x20000000} {
		p := provisionedProfile()
		k := p.Catalog[testRef]
		k.Nodes[2].ACL = append(k.Nodes[2].ACL, ACE{SID: testGuest, Mask: mask})
		p.Catalog[testRef] = k
		if loader, err := NewNative(p); loader != nil || err != ErrConfig {
			t.Fatal("untrusted leaf read/execute accepted")
		}
	}
}

func TestNativeCopiesProvisionedCatalog(t *testing.T) {
	p := provisionedProfile()
	loader, err := NewNative(p)
	if err != nil {
		t.Fatal(err)
	}
	k := p.Catalog[testRef]
	k.Nodes[0].ACL[0].SID = testGuest
	k.Nodes[1].OwnerSID = testGuest
	delete(p.Catalog, testRef)
	if loader.catalog[testRef].Nodes[0].ACL[0].SID == testGuest || loader.catalog[testRef].Nodes[1].OwnerSID == testGuest {
		t.Fatal("profile mutation changed loader")
	}
	signer, err := loader.Load(loadContext(t), testRef)
	if signer != nil || err != ErrClosed {
		t.Fatal("catalog mutation changed fixed reference")
	}
}
