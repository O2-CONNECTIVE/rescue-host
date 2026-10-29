package guestaccount

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// OWNER=S-1-5-18、DACL=Everyoneへread(1)を許可する合成self-relative descriptor。
func permissionDescriptorFixture() []byte {
	return []byte{
		1, 0, 4, 128, 20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 32, 0, 0, 0,
		1, 1, 0, 0, 0, 0, 0, 5, 18, 0, 0, 0,
		2, 0, 28, 0, 1, 0, 0, 0,
		0, 16, 20, 0, 1, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0,
	}
}

type permissionObjectFixtureKernel struct {
	data                                    []byte
	metadataValue                           permissionObjectMetadata
	calls                                   []string
	onCall                                  func(string)
	metadataErr, securityErr, descriptorErr error
	securitySize                            uint32
}

func (k *permissionObjectFixtureKernel) record(call string) {
	k.calls = append(k.calls, call)
	if k.onCall != nil {
		k.onCall(call)
	}
}
func (k *permissionObjectFixtureKernel) metadata(_ permissionObject, _ *permissionObjectWork, check func() bool) (permissionObjectMetadata, error) {
	if !check() {
		return permissionObjectMetadata{}, ErrDenied
	}
	k.record("metadata")
	return k.metadataValue, k.metadataErr
}
func (k *permissionObjectFixtureKernel) security(_ permissionObject, w *permissionObjectWork, check func() bool) (uint32, error) {
	if !check() {
		return 0, ErrDenied
	}
	k.record("security")
	copy(w.buffer, k.data)
	if k.securitySize != 0 {
		return k.securitySize, k.securityErr
	}
	return uint32(len(k.data)), k.securityErr
}
func (k *permissionObjectFixtureKernel) descriptor(_ []byte, _ permissionObjectDescriptor, check func() bool) error {
	if !check() {
		return ErrDenied
	}
	k.record("descriptor")
	return k.descriptorErr
}

func TestPermissionObjectReaderReturnsRawSecurityOnlyForSameHeldIdentity(t *testing.T) {
	k := &permissionObjectFixtureKernel{data: permissionDescriptorFixture(), metadataValue: permissionObjectMetadata{pid: 91, currentPID: 91, state: 2}}
	r := newPermissionObjectReader(k)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := r.read(ctx, permissionObject{role: roleService, kind: permissionObjectService, handle: 31}, func() bool { return true })
	if err != nil || s.role != roleService || s.kind != permissionObjectService || s.owner != "S-1-5-18" || s.pid != 91 || s.state != 2 || !reflect.DeepEqual(s.acl, []journalACE{{0, 16, 1, "S-1-1-0"}}) || r.unsafe() {
		t.Fatalf("同じ保持対象からraw securityを収集できません: %+v %v", s, err)
	}
	if !reflect.DeepEqual(k.calls, []string{"metadata", "security", "descriptor", "metadata"}) {
		t.Fatalf("同一性の前後照合を省略しました: %v", k.calls)
	}
}
