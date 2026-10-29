package guestaccount

import (
	"context"
	"testing"
)

type permissionAliasesFaultAPI struct {
	*permissionAliasesAPIFixture
	enumHook   func(*permissionAliasPage) error
	lookupHook func(*permissionAliasIdentity) error
	memberHook func(*permissionAliasPage) error
}

func (k *permissionAliasesFaultAPI) enumerate(p *permissionAliasPage) error {
	if e := k.permissionAliasesAPIFixture.enumerate(p); e != nil {
		return e
	}
	if k.enumHook != nil {
		return k.enumHook(p)
	}
	return nil
}
func (k *permissionAliasesFaultAPI) lookup(i *permissionAliasIdentity) error {
	if e := k.permissionAliasesAPIFixture.lookup(i); e != nil {
		return e
	}
	if k.lookupHook != nil {
		return k.lookupHook(i)
	}
	return nil
}
func (k *permissionAliasesFaultAPI) members(p *permissionAliasPage) error {
	if e := k.permissionAliasesAPIFixture.members(p); e != nil {
		return e
	}
	if k.memberHook != nil {
		return k.memberHook(p)
	}
	return nil
}

func TestPermissionAliasesRetainsAllBuffersOnCancelAndScopeLoss(t *testing.T) {
	for _, kind := range []string{"cancel", "scope", "partial-error", "free-error", "more-members"} {
		t.Run(kind, func(t *testing.T) {
			_, _, b := accountQueryTest(t)
			base := &permissionAliasesAPIFixture{memberSID: b.sid}
			api := &permissionAliasesFaultAPI{permissionAliasesAPIFixture: base}
			k := &permissionAliasesKernel{api: api}
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			healthy := true
			api.memberHook = func(p *permissionAliasPage) error {
				switch kind {
				case "cancel":
					cancel()
				case "scope":
					healthy = false
				case "partial-error":
					return ErrDenied
				case "more-members":
					p.status = 234
					p.resume = 91
				}
				return nil
			}
			if kind == "free-error" {
				base.freeFail = true
			}
			m, e := k.snapshot(ctx, b, func() bool { return healthy })
			if kind != "free-error" && e == nil {
				t.Fatal("partial source succeeded")
			}
			memory := m.(*permissionAliasesMemory)
			if k.inflight != m || len(memory.pages) != 2 || memory.pages[1].base == nil {
				t.Fatal("out parameters lost")
			}
			freeBefore := len(base.trace)
			e = m.release(func() bool { return healthy })
			if kind == "scope" {
				if e != ErrUnknown || len(base.trace) != freeBefore || memory.pages[1].base == nil {
					t.Fatal("free ran with lost actor", e)
				}
			} else if kind == "free-error" || kind == "more-members" {
				if e != ErrCleanup {
					t.Fatal("unknown cleanup acknowledged", e)
				}
			} else if e != nil {
				t.Fatal("healthy canceled read could not free", e)
			}
		})
	}
}
func TestPermissionAliasesKernelRetainsPanicOutParameters(t *testing.T) {
	_, _, b := accountQueryTest(t)
	api := &permissionAliasesAPIFixture{panicEnum: true}
	k := &permissionAliasesKernel{api: api}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("missing synthetic panic")
			}
		}()
		_, _ = k.snapshot(testContext(t), b, func() bool { return true })
	}()
	m, ok := k.inflight.(*permissionAliasesMemory)
	if !ok || !m.active || len(m.pages) != 1 {
		t.Fatal("panic lost live work")
	}
	if e := m.release(func() bool { return true }); e != ErrUnknown {
		t.Fatal("unreturned operation acknowledged", e)
	}
}
func TestPermissionAliasesRejectsAmbiguousForeignAndChangedAlias(t *testing.T) {
	for _, kind := range []string{"ambiguous", "foreign", "nonalias", "changed"} {
		t.Run(kind, func(t *testing.T) {
			_, _, b := accountQueryTest(t)
			api := &permissionAliasesFaultAPI{permissionAliasesAPIFixture: &permissionAliasesAPIFixture{memberSID: b.sid}}
			calls := 0
			api.lookupHook = func(i *permissionAliasIdentity) error {
				calls++
				if kind == "ambiguous" && calls == 1 {
					sid, _ := permissionFactsSID(b.machine + "-1200")
					copy(i.sid[:], sid)
					i.sidSize = uint32(len(sid))
					copy(i.domain[:], []uint16{'H', 'O', 'S', 'T', 0})
					i.domainSize = 4
					i.use = 4
					i.found = true
				}
				if i.found {
					switch kind {
					case "foreign":
						sid, _ := permissionFactsSID("S-1-5-21-90-91-92-1200")
						copy(i.sid[:], sid)
						i.sidSize = uint32(len(sid))
					case "nonalias":
						i.use = 1
					case "changed":
						if calls > 2 {
							sid, _ := permissionFactsSID("S-1-5-32-544")
							copy(i.sid[:], sid)
							i.sidSize = uint32(len(sid))
						}
					}
				}
				return nil
			}
			k := &permissionAliasesKernel{api: api}
			m, e := k.snapshot(testContext(t), b, func() bool { return true })
			if e == nil {
				t.Fatal("alias drift accepted")
			}
			if _, e = m.decode(); e == nil {
				t.Fatal("failed graph decoded")
			}
			if e = m.release(func() bool { return true }); e != nil {
				t.Fatal(e)
			}
		})
	}
}
