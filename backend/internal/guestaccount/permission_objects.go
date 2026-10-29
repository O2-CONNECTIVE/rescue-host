package guestaccount

import (
	"context"
	"runtime"
)

type permissionObjectKind uint8

const (
	permissionObjectService permissionObjectKind = iota + 1
	permissionObjectProcess
	permissionObjectToken
	permissionObjectPipe
	permissionObjectFile
	permissionObjectDirectory
)

// 同じ保護producerが保持するhandleを借用する。pathを開かず、handleを閉じない。
// roleやkindの指定だけではOS来歴を証明しない。provisioningは別途必要。
type permissionObject struct {
	role    permissionRole
	kind    permissionObjectKind
	handle  uintptr
	fileID  [16]byte
	volume  uint64
	userSID string
}

type permissionObjectSnapshot struct {
	role                              permissionRole
	kind                              permissionObjectKind
	owner                             string
	acl                               []journalACE
	directory, reparse, deletePending bool
	links                             uint32
	fileID                            [16]byte
	volume                            uint64
	pid, state, tokenType             uint32
	tokenUser                         string
}

type permissionObjectMetadata struct {
	directory, reparse, deletePending bool
	links, fileType                   uint32
	fileID                            [16]byte
	volume                            uint64
	pid, currentPID, state, tokenType uint32
	tokenUser                         string
}

// workはdispatchより先にreaderへ保持し、scope不明/panicならpinごと残す。
// 返却allocationを持つAPIを使わず、LocalFree/Close等のcleanupを追加しない。
type permissionObjectWork struct {
	buffer  []byte
	scratch [4096]byte
	needed  uint32
	pins    runtime.Pinner
}

type permissionObjectKernel interface {
	metadata(permissionObject, *permissionObjectWork, func() bool) (permissionObjectMetadata, error)
	security(permissionObject, *permissionObjectWork, func() bool) (uint32, error)
	descriptor([]byte, permissionObjectDescriptor, func() bool) error
}

// qのlocked singleworkerだけが使う。独自worker/gateや認証callbackを公開しない。
// 外側はOS threadを固定し、scopefatal時はreaderを強参照したままworkerを退役する。
type permissionObjectReader struct {
	self   *permissionObjectReader
	kernel permissionObjectKernel
	work   *permissionObjectWork
	object permissionObject
	fatal  bool
}

func newPermissionObjectReader(kernel permissionObjectKernel) *permissionObjectReader {
	r := &permissionObjectReader{kernel: kernel}
	r.self = r
	return r
}
func (r *permissionObjectReader) unsafe() bool {
	return r == nil || r.self != r || r.kernel == nil || r.fatal || r.work != nil
}
func (r *permissionObjectReader) scope(guard func() bool) (ok bool) {
	defer func() {
		if recover() != nil {
			r.fatal = true
			ok = false
		}
	}()
	if r.fatal || guard == nil || !guard() {
		r.fatal = true
		return false
	}
	return true
}
func (r *permissionObjectReader) read(ctx context.Context, object permissionObject, guard func() bool) (snapshot permissionObjectSnapshot, result error) {
	if r.unsafe() {
		return snapshot, ErrUnknown
	}
	if !finite(ctx) || !ordinaryPermissionObject(object) {
		return snapshot, ErrDenied
	}
	r.object = object
	w := &permissionObjectWork{buffer: make([]byte, 64*1024)}
	r.work = w
	defer func() {
		if recover() != nil {
			r.fatal = true
		}
		// ctx取消だけなら健康な同scopeでGo bufferのpinを解除できる。
		if !r.scope(guard) {
			snapshot, result = permissionObjectSnapshot{}, accountQueryScopeFatal{}
			return
		}
		w.pins.Unpin()
		r.work, r.object = nil, permissionObject{}
		if !finite(ctx) {
			snapshot, result = permissionObjectSnapshot{}, ErrDenied
		}
	}()
	w.pins.Pin(&w.buffer[0])
	w.pins.Pin(&w.scratch[0])
	w.pins.Pin(&w.needed)
	check := func() bool { return r.scope(guard) && finite(ctx) }
	if !check() {
		return snapshot, ErrDenied
	}
	before, err := r.kernel.metadata(object, w, check)
	if !check() || err != nil || !permissionObjectMatches(object, before) {
		return snapshot, ErrDenied
	}
	size, err := r.kernel.security(object, w, check)
	if !check() || err != nil || size < 20 || size > uint32(len(w.buffer)) {
		return snapshot, ErrDenied
	}
	data := w.buffer[:size]
	if !check() {
		return snapshot, ErrDenied
	}
	layout, err := permissionObjectDescriptorBounds(data)
	if err != nil || !check() {
		return snapshot, ErrDenied
	}
	if err := r.kernel.descriptor(data, layout, check); err != nil || !check() {
		return snapshot, ErrDenied
	}
	after, err := r.kernel.metadata(object, w, check)
	if !check() || err != nil || after != before || !permissionObjectMatches(object, after) {
		return snapshot, ErrDenied
	}
	return permissionObjectSnapshot{role: object.role, kind: object.kind, owner: layout.owner, acl: layout.acl,
		directory: after.directory, reparse: after.reparse, deletePending: after.deletePending, links: after.links, fileID: after.fileID, volume: after.volume,
		pid: after.pid, state: after.state, tokenType: after.tokenType, tokenUser: after.tokenUser}, nil
}

func ordinaryPermissionObject(o permissionObject) bool {
	if o.role == 0 || !realJournalHandle(o.handle) || o.kind < permissionObjectService || o.kind > permissionObjectDirectory {
		return false
	}
	if o.kind == permissionObjectToken {
		return serviceIdentity(o.userSID)
	}
	if o.kind == permissionObjectFile || o.kind == permissionObjectDirectory {
		return o.volume != 0 && o.fileID != ([16]byte{})
	}
	return true
}
func permissionObjectMatches(o permissionObject, m permissionObjectMetadata) bool {
	switch o.kind {
	case permissionObjectService:
		// START_PENDINGのPIDは不正確な場合もある。不一致は拒否するが同PIDを認証proofにしない。
		return (m.state == 2 || m.state == 4) && m.pid != 0 && m.pid == m.currentPID
	case permissionObjectProcess:
		return m.pid != 0 && m.pid == m.currentPID
	case permissionObjectToken:
		return m.tokenType == 1 && m.tokenUser == o.userSID
	case permissionObjectPipe:
		// FILE_TYPE_PIPEはsocketも含む。pipeの生成来歴は保護producerに依存する。
		return m.fileType == 3
	case permissionObjectFile, permissionObjectDirectory:
		return m.fileType == 1 && m.fileID == o.fileID && m.volume == o.volume && !m.reparse && !m.deletePending && m.links == 1 && m.directory == (o.kind == permissionObjectDirectory)
	}
	return false
}
