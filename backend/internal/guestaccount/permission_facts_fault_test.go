package guestaccount

import (
	"context"
	"encoding/binary"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func TestPermissionFactsUser4RejectsWrongIdentityAndMalformedAllocation(t *testing.T) {
	for _, kind := range []string{"small", "oversized", "nil-name", "outside-name", "unterminated", "unicode-name", "nil-sid", "outside-sid", "bad-sid", "foreign-name", "foreign-sid"} {
		t.Run(kind, func(t *testing.T) {
			data, b, put := permissionUser4Bytes(t)
			base := uintptr(unsafe.Pointer(&data[0]))
			size := uint32(len(data))
			sidOffset := 96
			if unsafe.Sizeof(uintptr(0)) == 8 {
				sidOffset = 152
			}
			switch kind {
			case "small":
				size = 1
			case "oversized":
				size = 65537
			case "nil-name":
				put(0, 0)
			case "outside-name":
				put(0, base+512)
			case "unterminated":
				binary.LittleEndian.PutUint16(data[296:], 65)
			case "unicode-name":
				binary.LittleEndian.PutUint16(data[256:], 0xd800)
			case "nil-sid":
				put(sidOffset, 0)
			case "outside-sid":
				put(sidOffset, base+508)
			case "bad-sid":
				data[320] = 2
			case "foreign-name":
				b.name = "foreign"
			case "foreign-sid":
				b.sid = b.machine + "-9999"
			}
			if o, e := decodePermissionUser4(&data[0], size, b); e == nil || o.primary != 0 || o.priv != 0 || o.authFlags != 0 || o.flags != 0 {
				t.Fatal("unbound native metadata escaped", kind, o, e)
			}
		})
	}
}

func TestPermissionFactsRightsStatusDoesNotConvertNativeErrorsToEmpty(t *testing.T) {
	for _, tc := range []struct {
		status, count      uint32
		data, empty, valid bool
	}{{0, 1, true, false, true}, {0, 0, false, true, true}, {0, 0, true, true, true}, {0, 1, false, false, false}, {0, 257, true, false, false}, {0xc0000034, 0, false, true, true}, {0xc0000034, 1, true, false, false}, {0xc0000034, 0, true, false, false}, {0xc0000022, 0, false, false, false}, {0xc000003a, 0, false, false, false}, {2, 0, false, false, false}, {0x105, 1, true, false, false}} {
		empty, e := permissionFactsRightsStatus(tc.status, tc.count, tc.data)
		if (e == nil) != tc.valid || e == nil && empty != tc.empty {
			t.Fatal("LSA status/ownership classification changed", tc, empty, e)
		}
	}
}

func TestPermissionFactsRightsRequireBoundedValidUTF16AndDeepOwnedLists(t *testing.T) {
	for _, units := range [][]uint16{nil, {0}, {0xd800}, {0xdc00}, {0xd800, 65}, {65, 10}, make([]uint16, 185)} {
		if _, e := permissionFactsRightString(units); e == nil {
			t.Fatal("malformed UTF16 accepted", units)
		}
	}
	if v, e := permissionFactsRightString([]uint16{0xd83d, 0xde00}); e != nil || v != "😀" {
		t.Fatal("valid surrogate pair replaced", v, e)
	}
	units := []uint16{'S', 'e', 'X'}
	pins := &runtime.Pinner{}
	pins.Pin(&units[0])
	defer pins.Unpin()
	for _, sizes := range [][2]uint16{{0, 0}, {1, 2}, {6, 4}, {6, 7}, {370, 370}} {
		if _, e := permissionFactsRightBuffer(sizes[0], sizes[1], &units[0]); e == nil {
			t.Fatal("invalid Unicode byte bounds accepted", sizes)
		}
	}
	if _, e := permissionFactsRightBuffer(2, 2, nil); e == nil {
		t.Fatal("nil Unicode buffer accepted")
	}
	v, e := permissionFactsRightBuffer(6, 6, &units[0])
	if e != nil || v != "SeX" {
		t.Fatal(v, e)
	}
	units[2] = 'Y'
	if v != "SeX" {
		t.Fatal("string borrowed native buffer")
	}
	for _, values := range [][]string{nil, {""}, {"SeX", "SeX"}, {"a\x00b"}, {string([]byte{0xff})}, make([]string, 257)} {
		if _, e := copyPermissionRights(values); e == nil {
			t.Fatal("malformed rights list accepted")
		}
	}
	if values, e := copyPermissionRights([]string{}); e != nil || values == nil || len(values) != 0 {
		t.Fatal("wellformed empty lost", values, e)
	}
}

func TestPermissionFactsRetainsPartialAndCanceledReturnedMemoryBeforeDecode(t *testing.T) {
	for _, which := range []string{"user", "rights"} {
		for _, kind := range []string{"partial-error", "cancel", "scope"} {
			t.Run(which+"/"+kind, func(t *testing.T) {
				q, f, b := accountQueryTest(t)
				m := &accountQueryFixtureMemory{f: f, label: "facts", value: accountQueryObservation{kind: accountQueryUser4, name: b.name, sid: b.sid, primary: 513, rights: []string{}}}
				k := &permissionFactsKernelFixture{user: m, assigned: m}
				c, e := newPermissionFactsCollector(q, k)
				if e != nil {
					t.Fatal(e)
				}
				ctx, cancel := context.WithCancel(testContext(t))
				defer cancel()
				guardCalls := 0
				guard := func() bool { guardCalls++; return kind != "scope" || guardCalls == 1 }
				if kind == "partial-error" {
					k.userErr, k.rightsErr = ErrDenied, ErrDenied
				}
				if kind == "cancel" {
					k.userHook, k.rightsHook = cancel, cancel
				}
				if which == "user" {
					_, e = c.user(ctx, b, guard)
				} else {
					_, e = c.assigned(ctx, "S-1-1-0", guard)
				}
				if e == nil || len(q.pending) != 1 || q.pending[0] != m {
					t.Fatal("partial memory lost before failure", e, len(q.pending))
				}
				if len(f.trace) != 0 {
					t.Fatal("failed native return was decoded or freed", f.trace)
				}
			})
		}
	}
}

func TestPermissionFactsRejectsNoncanonicalSIDAndForeignCollectorBeforeCalls(t *testing.T) {
	q, _, b := accountQueryTest(t)
	k := &permissionFactsKernelFixture{}
	c, e := newPermissionFactsCollector(q, k)
	if e != nil {
		t.Fatal(e)
	}
	for _, sid := range []string{"SY", "S-1-05-32-545", "S-2-5-32-545", "S-1-5-032-545", "S-1-5-4294967296", "S-1-281474976710656-0", "S-1-5", "S-1-5-0 ", "S-1-5-" + strings.Repeat("1-", 15) + "1"} {
		if _, e = c.assigned(testContext(t), sid, func() bool { return true }); e == nil {
			t.Fatal("noncanonical SID accepted", sid)
		}
	}
	foreign := b
	foreign.sid = b.machine + "-9999"
	if _, e = c.user(testContext(t), foreign, func() bool { return true }); e == nil {
		t.Fatal("foreign binding accepted")
	}
	copyC := permissionFactsCollector{self: c, query: q, kernel: k}
	if _, e = copyC.user(testContext(t), b, func() bool { return true }); e == nil {
		t.Fatal("copied collector used")
	}
	q.fatal = true
	if _, e = c.assigned(testContext(t), "S-1-1-0", func() bool { return true }); e == nil {
		t.Fatal("fatal query restarted")
	}
	if len(k.calls) != 0 {
		t.Fatal("pure rejection called native kernel", k.calls)
	}
}

func TestPermissionFactsCanonicalSIDEncodingAndUnknownRightsAreRawFacts(t *testing.T) {
	for _, sid := range []string{"S-1-1-0", "S-1-5-32-545", "S-1-5-21-10-20-30-1002"} {
		b, e := permissionFactsSID(sid)
		if e != nil {
			t.Fatal(e)
		}
		got, e := accountQuerySID(b)
		if e != nil || got != sid {
			t.Fatal("SID changed during binary encoding", got, e)
		}
	}
	if got, e := copyPermissionRights([]string{"SeUnknownFutureRight"}); e != nil || !reflect.DeepEqual(got, []string{"SeUnknownFutureRight"}) {
		t.Fatal("leaf reader inferred policy or lost unknown right", got, e)
	}
}

func TestPermissionFactsSourcePanicKeepsInflightReferenceAndMultiPartFreeGuards(t *testing.T) {
	q, f, b := accountQueryTest(t)
	m := &accountQueryFixtureMemory{f: f, label: "rights", nativeParts: 2, value: accountQueryObservation{kind: accountQueryRights, sid: "S-1-1-0", rights: []string{}}}
	k := &permissionFactsKernelFixture{user: m, assigned: m}
	c, e := newPermissionFactsCollector(q, k)
	if e != nil {
		t.Fatal(e)
	}
	k.userHook = func() { panic("synthetic native return") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic silently accepted")
			}
		}()
		_, _ = c.user(testContext(t), b, func() bool { return true })
	}()
	if k.inflight != m || len(q.pending) != 0 {
		t.Fatal("unreturned source reference lost")
	}
	k.userHook = nil
	if _, e = c.assigned(testContext(t), "S-1-1-0", func() bool { return true }); e != nil {
		t.Fatal(e)
	}
	healthy := true
	f.releaseHook = func(string) { healthy = false }
	guard := func() bool {
		if !healthy {
			q.fatal = true
		}
		return healthy
	}
	if q.releasePending(guard) || len(q.pending) != 1 {
		t.Fatal("scope loss during compound release was accepted")
	}
	for _, step := range f.trace {
		if step == "free:rights:next" {
			t.Fatal("second native release after scope loss")
		}
	}
}

func TestPermissionFactsEveryNativePhaseChecksCancellationAndRawScope(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	rawCalls := 0
	raw := func() bool { rawCalls++; return true }
	p := permissionFactsPhase{ctx: ctx, guard: raw}
	if !p.allowed() || !p.allowed() || rawCalls != 2 {
		t.Fatal("each native phase must query raw scope")
	}
	cancel()
	if p.allowed() {
		t.Fatal("cancellation must stop next native phase")
	}
	// 照会の cancel が健康な元 scope による後始末を妨げない。
	if !raw() {
		t.Fatal("raw cleanup scope was canceled")
	}
	if (permissionFactsPhase{ctx: testContext(t)}).allowed() {
		t.Fatal("missing raw guard accepted")
	}
	if (permissionFactsPhase{ctx: testContext(t), guard: func() bool { return false }}).allowed() {
		t.Fatal("dirty scope accepted")
	}
	ctx2, cancel2 := context.WithCancel(testContext(t))
	if (permissionFactsPhase{ctx: ctx2, guard: func() bool { cancel2(); return true }}).allowed() {
		t.Fatal("guard-time cancellation accepted")
	}
}
