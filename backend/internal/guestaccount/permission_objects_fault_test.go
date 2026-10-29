package guestaccount

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPermissionObjectReaderRejectsMalformedDescriptorBeforeSDKParsing(t *testing.T) {
	for name, change := range map[string]func([]byte) []byte{
		"short header":         func(b []byte) []byte { return b[:19] },
		"absolute":             func(b []byte) []byte { b[3] = 0; return b },
		"missing dacl":         func(b []byte) []byte { b[2] = 0; return b },
		"null dacl":            func(b []byte) []byte { clear(b[16:20]); return b },
		"owner header pointer": func(b []byte) []byte { b[4] = 4; return b },
		"owner overflow":       func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:], ^uint32(0)); return b },
		"owner sid count":      func(b []byte) []byte { b[21] = 16; return b },
		"unrequested sacl":     func(b []byte) []byte { b[12] = 32; return b },
		"acl size":             func(b []byte) []byte { b[34] = 29; return b },
		"acl count":            func(b []byte) []byte { b[36] = 129; return b },
		"unknown ace":          func(b []byte) []byte { b[40] = 2; return b },
		"ace size":             func(b []byte) []byte { b[42] = 24; return b },
		"ace sid extent":       func(b []byte) []byte { b[49] = 2; return b },
	} {
		t.Run(name, func(t *testing.T) {
			k := &permissionObjectFixtureKernel{data: change(permissionDescriptorFixture()), metadataValue: permissionObjectMetadata{pid: 91, currentPID: 91}}
			r := newPermissionObjectReader(k)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s, err := r.read(ctx, permissionObject{role: roleSupervisorProcess, kind: permissionObjectProcess, handle: 31}, func() bool { return true })
			if !errors.Is(err, ErrDenied) || !reflect.DeepEqual(s, permissionObjectSnapshot{}) || r.unsafe() || !reflect.DeepEqual(k.calls, []string{"metadata", "security"}) {
				t.Fatalf("不正descriptorをnative SDKまたは成功へ渡しました: %+v %v %v", s, err, k.calls)
			}
		})
	}
}

func TestPermissionObjectReaderPreservesRawFlagsMasksAndKnownEmptyDACL(t *testing.T) {
	for _, empty := range []bool{false, true} {
		data := permissionDescriptorFixture()
		if empty {
			data = data[:40]
			data[34], data[36] = 8, 0
		} else {
			data[40], data[41] = 1, 255
			binary.LittleEndian.PutUint32(data[44:], 0xffffffff)
		}
		k := &permissionObjectFixtureKernel{data: data, metadataValue: permissionObjectMetadata{fileType: 3}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, err := newPermissionObjectReader(k).read(ctx, permissionObject{role: roleControlPipe, kind: permissionObjectPipe, handle: 31}, func() bool { return true })
		cancel()
		if err != nil || s.acl == nil {
			t.Fatalf("確認済みDACLを未知と混同しました: %v", err)
		}
		if empty && len(s.acl) != 0 || !empty && !reflect.DeepEqual(s.acl, []journalACE{{1, 255, 0xffffffff, "S-1-1-0"}}) {
			t.Fatalf("raw ACEをpolicy判断前に書き換えました: %v", s.acl)
		}
	}
}

func TestPermissionObjectReaderRetainsPinsAndRefusesReuseAfterScopeLossOrPanic(t *testing.T) {
	for _, mode := range []string{"scope loss", "guard panic", "native panic", "parser panic"} {
		t.Run(mode, func(t *testing.T) {
			lost := false
			k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: permissionObjectMetadata{pid: 91, currentPID: 91}}
			k.onCall = func(call string) {
				if call == "security" && mode != "parser panic" {
					if mode == "native panic" {
						panic("synthetic native panic")
					}
					lost = true
				}
				if call == "descriptor" && mode == "parser panic" {
					panic("synthetic parser panic")
				}
			}
			guard := func() bool {
				if lost && mode == "guard panic" {
					panic("synthetic guard panic")
				}
				return !lost
			}
			r := newPermissionObjectReader(k)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			o := permissionObject{role: roleSupervisorProcess, kind: permissionObjectProcess, handle: 31}
			s, err := r.read(ctx, o, guard)
			var fatal accountQueryScopeFatal
			if !errors.As(err, &fatal) || !errors.Is(err, ErrUnknown) || !reflect.DeepEqual(s, permissionObjectSnapshot{}) || !r.unsafe() || r.work == nil || r.object.handle != 31 {
				t.Fatalf("不明scopeのpin/参照を失いました: %+v %v", s, err)
			}
			n := len(k.calls)
			if _, err := r.read(ctx, o, func() bool { return true }); !errors.Is(err, ErrUnknown) || len(k.calls) != n {
				t.Fatal("fatal後にnative読取を再開しました")
			}
			copied := *r
			if _, err := copied.read(ctx, o, func() bool { return true }); !errors.Is(err, ErrUnknown) || len(k.calls) != n || r.work == nil {
				t.Fatal("reader copyで未知参照を消失しました")
			}
			// テストのfakeはnative参照がないため、この後はfixtureだけpinを解除する。
			r.work.pins.Unpin()
		})
	}
}

func TestPermissionObjectReaderSeparatesCancellationFromLostScope(t *testing.T) {
	for _, lost := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: permissionObjectMetadata{fileType: 3}}
		cancelled := false
		checksAfterCancel := 0
		k.onCall = func(call string) {
			if call == "security" {
				cancelled = true
				cancel()
			}
		}
		guard := func() bool {
			if cancelled {
				checksAfterCancel++
				return !lost
			}
			return true
		}
		r := newPermissionObjectReader(k)
		s, err := r.read(ctx, permissionObject{role: roleControlPipe, kind: permissionObjectPipe, handle: 31}, guard)
		cancel()
		if err == nil || !reflect.DeepEqual(s, permissionObjectSnapshot{}) || checksAfterCancel == 0 || !reflect.DeepEqual(k.calls, []string{"metadata", "security"}) {
			t.Fatalf("取消後のscope照合を省略/取得結果を採用しました: %+v %v %v", s, err, k.calls)
		}
		if lost {
			var fatal accountQueryScopeFatal
			if !errors.As(err, &fatal) || !r.unsafe() || r.work == nil {
				t.Fatal("ctx取消でscope不明を隠しました")
			}
			r.work.pins.Unpin() // native資源を持たない合成fixtureだけの回収。
		} else if !errors.Is(err, ErrDenied) || r.unsafe() || r.work != nil {
			t.Fatal("健康なscopeの取消をfatal/資源残存へ変えました")
		}
	}
}

func TestPermissionObjectReaderRefusesForeignChangedOrUnsupportedMetadata(t *testing.T) {
	file := permissionObject{role: roleConfiguration, kind: permissionObjectFile, handle: 31, fileID: [16]byte{1}, volume: 2}
	fileMeta := permissionObjectMetadata{fileType: 1, fileID: [16]byte{1}, volume: 2, links: 1}
	for name, value := range map[string]struct {
		object   permissionObject
		metadata permissionObjectMetadata
		changed  bool
	}{
		"service stopped":             {permissionObject{role: roleService, kind: permissionObjectService, handle: 31}, permissionObjectMetadata{pid: 91, currentPID: 91, state: 1}, false},
		"service foreign pid":         {permissionObject{role: roleService, kind: permissionObjectService, handle: 31}, permissionObjectMetadata{pid: 92, currentPID: 91, state: 4}, false},
		"process foreign pid":         {permissionObject{role: roleSupervisorProcess, kind: permissionObjectProcess, handle: 31}, permissionObjectMetadata{pid: 92, currentPID: 91}, false},
		"impersonation token":         {permissionObject{role: roleSupervisorToken, kind: permissionObjectToken, handle: 31, userSID: "S-1-5-18"}, permissionObjectMetadata{tokenType: 2, tokenUser: "S-1-5-18"}, false},
		"foreign token user":          {permissionObject{role: roleSupervisorToken, kind: permissionObjectToken, handle: 31, userSID: "S-1-5-18"}, permissionObjectMetadata{tokenType: 1, tokenUser: "S-1-5-19"}, false},
		"non pipe":                    {permissionObject{role: roleControlPipe, kind: permissionObjectPipe, handle: 31}, permissionObjectMetadata{fileType: 1}, false},
		"file changed after security": {file, fileMeta, true},
		"service state changed":       {permissionObject{role: roleService, kind: permissionObjectService, handle: 31}, permissionObjectMetadata{pid: 91, currentPID: 91, state: 2}, true},
	} {
		t.Run(name, func(t *testing.T) {
			k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: value.metadata}
			metadataCalls := 0
			k.onCall = func(call string) {
				if call == "metadata" {
					metadataCalls++
				}
				if value.changed && metadataCalls == 2 {
					if value.object.kind == permissionObjectService {
						k.metadataValue.state = 4
					} else {
						k.metadataValue.fileID[0] = 2
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := newPermissionObjectReader(k)
			if s, err := r.read(ctx, value.object, func() bool { return true }); !errors.Is(err, ErrDenied) || !reflect.DeepEqual(s, permissionObjectSnapshot{}) || r.unsafe() {
				t.Fatalf("未知/別対象のmetadataを採用しました: %+v %v", s, err)
			}
		})
	}
	for name, change := range map[string]func(*permissionObjectMetadata){
		"different id":          func(m *permissionObjectMetadata) { m.fileID[0]++ },
		"different volume":      func(m *permissionObjectMetadata) { m.volume++ },
		"not disk":              func(m *permissionObjectMetadata) { m.fileType = 3 },
		"reparse":               func(m *permissionObjectMetadata) { m.reparse = true },
		"hardlink":              func(m *permissionObjectMetadata) { m.links = 2 },
		"delete pending":        func(m *permissionObjectMetadata) { m.deletePending = true },
		"directory substituted": func(m *permissionObjectMetadata) { m.directory = true },
	} {
		t.Run(name, func(t *testing.T) {
			m := fileMeta
			change(&m)
			k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: m}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := newPermissionObjectReader(k).read(ctx, file, func() bool { return true }); !errors.Is(err, ErrDenied) || !reflect.DeepEqual(k.calls, []string{"metadata"}) {
				t.Fatalf("不正なfileからsecurityを読みました: %v %v", err, k.calls)
			}
		})
	}
}

func TestPermissionObjectReaderAcceptsSupportedKindsAndRejectsUnconfiguredBeforeCalls(t *testing.T) {
	for _, value := range []struct {
		object   permissionObject
		metadata permissionObjectMetadata
	}{
		{permissionObject{role: roleService, kind: permissionObjectService, handle: 31}, permissionObjectMetadata{pid: 91, currentPID: 91, state: 4}},
		{permissionObject{role: roleSupervisorToken, kind: permissionObjectToken, handle: 31, userSID: "S-1-5-18"}, permissionObjectMetadata{tokenType: 1, tokenUser: "S-1-5-18"}},
		{permissionObject{role: roleArtifact, kind: permissionObjectFile, handle: 31, fileID: [16]byte{1}, volume: 2}, permissionObjectMetadata{fileID: [16]byte{1}, volume: 2, fileType: 1, links: 1}},
		{permissionObject{role: roleAncestor, kind: permissionObjectDirectory, handle: 31, fileID: [16]byte{1}, volume: 2}, permissionObjectMetadata{fileID: [16]byte{1}, volume: 2, fileType: 1, links: 1, directory: true}},
	} {
		k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: value.metadata}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if _, err := newPermissionObjectReader(k).read(ctx, value.object, func() bool { return true }); err != nil {
			t.Errorf("対応kindの合成収集が失敗しました: %v", err)
		}
		cancel()
	}
	for _, object := range []permissionObject{{}, {role: roleService, kind: 7, handle: 31}, {role: roleSupervisorProcess, kind: permissionObjectProcess, handle: ^uintptr(0)}, {role: roleArtifact, kind: permissionObjectFile, handle: 31}, {role: roleSupervisorToken, kind: permissionObjectToken, handle: 31}} {
		k := &permissionObjectFixtureKernel{}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if _, err := newPermissionObjectReader(k).read(ctx, object, func() bool { return true }); !errors.Is(err, ErrDenied) || len(k.calls) != 0 {
			t.Error("未設定/未対応対象でkernelを呼びました")
		}
		cancel()
	}
}

func TestPermissionObjectReaderNeverUsesPartialErrorOrOversizedNativeOutput(t *testing.T) {
	for _, at := range []string{"metadata", "security", "descriptor", "oversized"} {
		k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: permissionObjectMetadata{fileType: 3}}
		synthetic := errors.New("synthetic read failure")
		switch at {
		case "metadata":
			k.metadataErr = synthetic
		case "security":
			k.securityErr = synthetic
		case "descriptor":
			k.descriptorErr = synthetic
		case "oversized":
			k.securitySize = 64*1024 + 1
		}
		r := newPermissionObjectReader(k)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, err := r.read(ctx, permissionObject{role: roleControlPipe, kind: permissionObjectPipe, handle: 31}, func() bool { return true })
		cancel()
		if !errors.Is(err, ErrDenied) || !reflect.DeepEqual(s, permissionObjectSnapshot{}) || r.unsafe() {
			t.Fatalf("部分結果/未知長を採用しました: %+v %v", s, err)
		}
		for _, call := range k.calls {
			if (at == "metadata" || at == "security" || at == "oversized") && call == "descriptor" {
				t.Fatal("失敗結果をSDK parserへ渡しました")
			}
		}
	}
}

func TestPermissionObjectReaderKeepsOutputPinnedUntilNativeReadActuallyReturns(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: permissionObjectMetadata{fileType: 3}}
	k.onCall = func(call string) {
		if call == "security" {
			close(entered)
			<-release
		}
	}
	r := newPermissionObjectReader(k)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.read(ctx, permissionObject{role: roleControlPipe, kind: permissionObjectPipe, handle: 31}, func() bool { return true })
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("合成native callへ到達しません")
	}
	cancel()
	if r.work == nil || len(r.work.buffer) != 64*1024 || r.object.handle != 31 {
		t.Fatal("実call中のoutbuffer参照を保持しません")
	}
	select {
	case <-done:
		t.Fatal("native実returnより先に取消完了を返しました")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrDenied) || r.unsafe() {
			t.Fatalf("実return後の健康な取消回収が不正です: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("合成call実return後に終了を確認できません")
	}
}
