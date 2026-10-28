package serviceprovision

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"testing"
)

func launchConfig() serviceready.Config {
	r := validRecord()
	return serviceready.Config{Profile: r.Profile, PrimaryToken: 73, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout}
}
func TestLaunchFactoryRejectsUnconfiguredAndOnlyConstructsStatically(t *testing.T) {
	if f, err := NewNativeLaunchFactory(serviceready.Config{}); f != nil || !errors.Is(err, ErrLaunchConfig) {
		t.Fatal("zero launch config", err)
	}
	var f LaunchFactory
	if owner, err := f.Prepare(rootContext(t)); owner != nil || !errors.Is(err, ErrLaunchConfig) {
		t.Fatal("zero launch factory", err)
	}
	if f, err := NewNativeLaunchFactory(launchConfig()); f == nil || err != nil {
		t.Fatal("pure launch constructor", err)
	}
}

type launchFakePreflight struct {
	binding        serviceready.ProcessBinding
	checks, closes int
	err            error
}

func (p *launchFakePreflight) Recheck(context.Context) error { p.checks++; return p.err }
func (p *launchFakePreflight) Binding(context.Context) (serviceready.ProcessBinding, error) {
	return p.binding, p.err
}
func (p *launchFakePreflight) Close(context.Context) error { p.closes++; return nil }

type launchFakeRef struct {
	number      uintptr
	mask, flags uint32
	protection  launchProtection
	closes      int
	closeErr    error
}

func (r *launchFakeRef) value() uintptr                       { return r.number }
func (r *launchFakeRef) permissions() (uint32, uint32, error) { return r.mask, r.flags, nil }
func (r *launchFakeRef) protect() (launchProtection, error)   { return r.protection, nil }
func (r *launchFakeRef) close() error                         { r.closes++; return r.closeErr }

type launchFakeThread struct {
	state  launchThreadState
	closes int
}

func (t *launchFakeThread) inspect() (launchThreadState, error) { return t.state, nil }
func (t *launchFakeThread) close() error                        { t.closes++; return nil }

type launchFakeSnapshot struct {
	ids    []uint32
	closes int
}

func (s *launchFakeSnapshot) threads(context.Context, uint32) ([]uint32, error) {
	return append([]uint32(nil), s.ids...), nil
}
func (s *launchFakeSnapshot) close() error { s.closes++; return nil }

type launchFakeKernel struct {
	pre              launchFakePreflight
	identityValue    rootPrincipal
	security, strong launchFakeRef
	threads          map[uint32]*launchFakeThread
	ids              []uint32
	snapshots        []*launchFakeSnapshot
	strongOpens      int
	onStrong         func()
}

func (k *launchFakeKernel) threadID() uint32                                   { return 7 }
func (k *launchFakeKernel) currentPID() uint32                                 { return 55 }
func (k *launchFakeKernel) preflight(context.Context) (launchPreflight, error) { return &k.pre, nil }
func (k *launchFakeKernel) identity(uintptr) (rootPrincipal, error)            { return k.identityValue, nil }
func (k *launchFakeKernel) openToken(mask uint32) (launchTokenRef, error) {
	if mask == 0xb {
		k.strongOpens++
		if k.onStrong != nil {
			k.onStrong()
		}
		return &k.strong, nil
	}
	return &k.security, nil
}
func (k *launchFakeKernel) snapshot() (launchSnapshot, error) {
	s := &launchFakeSnapshot{ids: append([]uint32(nil), k.ids...)}
	k.snapshots = append(k.snapshots, s)
	return s, nil
}
func (k *launchFakeKernel) openThread(id uint32) (launchThreadRef, error) { return k.threads[id], nil }
func launchFixture(c serviceready.Config) *launchFakeKernel {
	security := launchObjectSecurity{owner: c.Profile.Keys.ProcessUserSID, acl: []protectedkey.ACE{{SID: c.Profile.Keys.ServiceSID, Mask: 0x10000000}}}
	k := &launchFakeKernel{pre: launchFakePreflight{binding: serviceready.ProcessBinding{PID: 55, Creation: 42}}, identityValue: rootPrincipal{user: c.Profile.Keys.ProcessUserSID, kind: 1, tokenID: 3, authenticationID: 4, modifiedID: 5, groups: []rootGroup{{sid: c.Profile.Keys.ServiceSID, attributes: 4}}}, ids: []uint32{7, 8}, threads: map[uint32]*launchFakeThread{}}
	k.security = launchFakeRef{number: 74, mask: 0x20008, protection: launchProtection{object: security, defaultOwner: c.Profile.Keys.ProcessUserSID, defaultACL: []protectedkey.ACE{{SID: c.Profile.Keys.ServiceSID, Mask: 0x10000000}}}}
	k.strong = launchFakeRef{number: 75, mask: 0xb}
	for _, id := range k.ids {
		k.threads[id] = &launchFakeThread{state: launchThreadState{pid: 55, tid: id, creation: uint64(id) + 100, alive: true, access: 0x120800, security: security}}
	}
	return k
}
func TestUnsafeTokenProtectionNeverOpensStrongHandle(t *testing.T) {
	c := launchConfig()
	k := launchFixture(c)
	f, err := NewNativeLaunchFactory(c)
	if err != nil {
		t.Fatal(err)
	}
	f.api = k
	k.security.protection.object.acl = append(k.security.protection.object.acl, protectedkey.ACE{Type: 1, SID: c.Profile.Keys.GuestSID, Mask: 0xf01ff}, protectedkey.ACE{SID: c.Profile.Keys.GuestSID, Mask: 2})
	owner, err := f.Prepare(rootContext(t))
	if owner == nil || err == nil || k.strongOpens != 0 {
		t.Fatal("unsafe protection opened strong handle", err, k.strongOpens)
	}
	if value, err := owner.Borrow(rootContext(t)); value != 0 || err == nil {
		t.Fatal("partial owner became accepted")
	}
	if err = owner.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.security.closes != 1 || k.pre.closes != 1 || k.strong.closes != 0 {
		t.Fatal("partial references lost")
	}
}
func TestProtectedTokenAndAllExistingThreadsPermitOnlyMinimumStrongHandle(t *testing.T) {
	c := launchConfig()
	k := launchFixture(c)
	f, err := NewNativeLaunchFactory(c)
	if err != nil {
		t.Fatal(err)
	}
	f.api = k
	c.Profile.Keys.ProcessUserSID = "S-1-5-21-9"
	c.ServiceSecurity.ACL[0].Mask = 0
	owner, err := f.Prepare(rootContext(t))
	if owner == nil || err != nil {
		t.Fatal("protected owner", err)
	}
	if k.strongOpens != 1 {
		t.Fatal("strong open count", k.strongOpens)
	}
	value, err := owner.Borrow(rootContext(t))
	if value != 75 || err != nil {
		t.Fatal(value, err)
	}
	if err = owner.Recheck(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if err = owner.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.strong.closes != 1 || k.security.closes != 1 || k.pre.closes != 1 {
		t.Fatal("token/preflight owners lost")
	}
	for _, thread := range k.threads {
		if thread.closes != 1 {
			t.Fatal("held thread lost")
		}
	}
	for _, snapshot := range k.snapshots {
		if snapshot.closes != 1 {
			t.Fatal("snapshot owner lost")
		}
	}
	if value, err = owner.Borrow(rootContext(t)); value != 0 || err == nil {
		t.Fatal("closed owner borrowed")
	}
}

func TestLaunchRejectsZeroStrongReference(t *testing.T) {
	c := launchConfig()
	k := launchFixture(c)
	k.strong.number = 0
	f, _ := NewNativeLaunchFactory(c)
	f.api = k
	owner, err := f.Prepare(rootContext(t))
	if owner == nil || err == nil {
		t.Fatal("zero strong handle accepted", err)
	}
	if value, err := owner.Borrow(rootContext(t)); value != 0 || err == nil {
		t.Fatal("zero handle became accepted")
	}
	if err := owner.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
}

// 既存sourceへの負例回帰。OS APIを呼ばず公開factoryから全所有経路を通す。
func TestLaunchProtectionAndBindingFailuresStayRevoked(t *testing.T) {
	unknown := "S-1-5-21-99"
	for _, tc := range []struct {
		name        string
		change      func(*launchFakeKernel)
		strongOpens int
		fatal       bool
	}{
		{"unknown generic all", func(k *launchFakeKernel) {
			k.security.protection.object.acl = []protectedkey.ACE{{SID: unknown, Mask: 0x10000000}}
		}, 0, false},
		{"unknown generic read default", func(k *launchFakeKernel) {
			k.security.protection.defaultACL = []protectedkey.ACE{{SID: unknown, Mask: 0x80000000}}
		}, 0, false},
		{"untrusted token owner", func(k *launchFakeKernel) { k.security.protection.object.owner = unknown }, 0, false},
		{"untrusted default owner", func(k *launchFakeKernel) { k.security.protection.defaultOwner = unknown }, 0, false},
		{"null default dacl", func(k *launchFakeKernel) { k.security.protection.defaultACL = nil }, 0, false},
		{"maximum allowed trusted", func(k *launchFakeKernel) { k.security.protection.defaultACL[0].Mask = 0x02000000 }, 0, false},
		{"unknown ace", func(k *launchFakeKernel) { k.security.protection.defaultACL[0].Type = 5 }, 0, false},
		{"unsafe existing thread", func(k *launchFakeKernel) {
			k.threads[8].state.security.acl = []protectedkey.ACE{{Type: 1, SID: unknown, Mask: 0x1fffff}, {SID: unknown, Mask: 0x10}}
		}, 0, false},
		{"thread new set after strong", func(k *launchFakeKernel) { k.onStrong = func() { k.ids = append(k.ids, 9) } }, 1, false},
		{"process creation after strong", func(k *launchFakeKernel) { k.onStrong = func() { k.pre.binding.Creation++ } }, 1, false},
		{"thread creation after strong", func(k *launchFakeKernel) { k.onStrong = func() { k.threads[8].state.creation++ } }, 1, false},
		{"token modified id after strong", func(k *launchFakeKernel) { k.onStrong = func() { k.identityValue.modifiedID++ } }, 1, true},
		{"strong inherited", func(k *launchFakeKernel) { k.strong.flags = 1 }, 1, false},
		{"strong excess rights", func(k *launchFakeKernel) { k.strong.mask |= 4 }, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := launchConfig()
			k := launchFixture(c)
			tc.change(k)
			f, _ := NewNativeLaunchFactory(c)
			f.api = k
			owner, err := f.Prepare(rootContext(t))
			if owner == nil || err == nil || k.strongOpens != tc.strongOpens || errors.Is(err, ErrLaunchFatal) != tc.fatal {
				t.Fatal("unsafe evidence accepted or classified incorrectly", err, k.strongOpens)
			}
			if value, err := owner.Borrow(rootContext(t)); value != 0 || err == nil {
				t.Fatal("failed owner revived")
			}
			if err := owner.Close(rootContext(t)); (err != nil) != tc.fatal || errors.Is(err, ErrLaunchFatal) != tc.fatal {
				t.Fatal("cleanup lost fatal classification", err)
			}
			if k.security.closes != 1 || k.pre.closes != 1 || k.strong.closes != tc.strongOpens {
				t.Fatal("partial resources lost")
			}
		})
	}
}

func TestLaunchCloseRetainsUnknownReferenceForSameOwnerRetry(t *testing.T) {
	c := launchConfig()
	k := launchFixture(c)
	f, _ := NewNativeLaunchFactory(c)
	f.api = k
	owner, err := f.Prepare(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.strong.closeErr = ErrLaunchUnconfirmed
	if err := owner.Close(rootContext(t)); !errors.Is(err, ErrLaunchUnconfirmed) || owner.strong == nil {
		t.Fatal("unknown close dropped owned token", err)
	}
	if value, err := owner.Borrow(rootContext(t)); value != 0 || err == nil {
		t.Fatal("cleanup-only owner borrowed")
	}
	k.strong.closeErr = nil
	if err := owner.Close(rootContext(t)); err != nil || owner.strong != nil || k.strong.closes != 2 || k.security.closes != 1 || k.pre.closes != 1 {
		t.Fatal("same owner did not retry only held reference", err)
	}
}
