package guestaccount

import (
	"context"
	"errors"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

func controlInspectionFixture() *permissionObjectFixtureKernel {
	data := permissionDescriptorFixture()
	data[41], data[44], data[46] = 0, 0, 2 // EveryoneへREAD_CONTROLだけの合成DACL。
	return &permissionObjectFixtureKernel{data: data, metadataValue: permissionObjectMetadata{fileType: 3}}
}

func TestStagingControlQueryReturnsLeaseBeforeLongLivedSession(t *testing.T) {
	p, c := stagingProfileFixture(t)
	f := &stagingControlFixture{h: 111}
	r := &stagingControlRef{lease: f}
	k := controlInspectionFixture()
	reader := newPermissionObjectReader(k)
	k.onCall = func(string) {
		if f.closes != 0 {
			t.Fatal("SDK照会終了より前にlease返却")
		}
	}
	if e := r.verifyAndRelease(testContext(t), p.binding, c.serviceSID, reader, func() bool { return true }); e != nil {
		t.Fatal(e)
	}
	if f.closes != 1 || r.handle != 0 || !r.closed || len(k.calls) != 4 || reader.unsafe() {
		t.Fatal("同endpoint実照会とlogical lease返却が完了せず")
	}
	calls, checks := len(k.calls), f.checks
	if e := r.verifyAndRelease(testContext(t), p.binding, c.serviceSID, reader, func() bool { return true }); e != nil || len(k.calls) != calls || f.checks != checks {
		t.Fatal("長期検査で退役した要求pipeへ再入", e)
	}
}

func TestStagingControlQueryDeniesUnsafePipeAndCancellation(t *testing.T) {
	for _, kind := range []string{"write-access", "wrong-kind", "changed-handle", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			p, c := stagingProfileFixture(t)
			f := &stagingControlFixture{h: 111}
			r := &stagingControlRef{lease: f}
			k := controlInspectionFixture()
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			switch kind {
			case "write-access":
				k.data[44] = 2
			case "wrong-kind":
				k.metadataValue.fileType = 1
			case "changed-handle":
				k.onCall = func(string) { f.h = 112 }
			case "canceled":
				k.onCall = func(string) { cancel() }
			}
			if e := r.verifyAndRelease(ctx, p.binding, c.serviceSID, newPermissionObjectReader(k), func() bool { return true }); e == nil {
				t.Fatal("不正pipe/取消を検証済み")
			}
			if f.closes != 0 {
				t.Fatal("失敗時に外側ownerより先に返却")
			}
			if e := r.close(testContext(t)); e != nil || f.closes != 1 {
				t.Fatal("健康なscopeでの部分取得回収なし", e)
			}
		})
	}
}

func TestStagingControlDirtyQueryKeepsLeaseAndPinsAfterScopeRestores(t *testing.T) {
	p, c := stagingProfileFixture(t)
	f := &stagingControlFixture{h: 111}
	r := &stagingControlRef{lease: f}
	k := controlInspectionFixture()
	healthy := true
	k.onCall = func(string) { healthy = false }
	reader := newPermissionObjectReader(k)
	if e := r.verifyAndRelease(testContext(t), p.binding, c.serviceSID, reader, func() bool { return healthy }); !errors.Is(e, bootstrapauth.ErrFatal) {
		t.Fatal("不明scopeをfatalへ固定せず", e)
	}
	healthy = true
	calls, checks := len(k.calls), f.checks
	if e := r.close(testContext(t)); !errors.Is(e, bootstrapauth.ErrFatal) || f.closes != 0 || !reader.unsafe() {
		t.Fatal("不明queryのlease/pinを解放", e)
	}
	_ = r.verifyAndRelease(testContext(t), p.binding, c.serviceSID, reader, func() bool { return healthy })
	if len(k.calls) != calls || f.checks != checks {
		t.Fatal("scope復元後にnativeを再開")
	}
}
