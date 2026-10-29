package guestaccount

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync"
	"sync/atomic"
)

const journalBasename = "guest-journal-v1.bin"
const journalHeaderBytes = 80
const journalFrameBytes = 2048
const journalMaxFrames = 1024
const journalIntentFrames = 6 // reserve 自身を含む最長の cleanup 経路。

type journalPending struct{}

func (journalPending) Error() string { return "journal の native 操作が未完了です" }

type journalIdentity struct {
	volume          uint64
	fileID          [16]byte
	owner, security string
}
type journalFileInfo struct {
	identity                               journalIdentity
	size                                   uint64
	kind, links                            uint32
	directory, reparse, inherit, protected bool
}

// 保護 root producer は未設定。raw handle や caller の成功 bool から発行しない。
type journalRootCapability struct {
	self                         *journalRootCapability
	root                         uintptr
	installation                 [32]byte
	machine, service, serviceSID string
	thread                       uint32
	rootID, ledgerID             journalIdentity
}

type journalKernel interface {
	subject(journalRootCapability) error
	inspect(uintptr) (journalFileInfo, error)
	open(uintptr) (uintptr, error)
	lock(uintptr) error
	read(uintptr, []byte, uint64) (int, error)
	write(uintptr, []byte, uint64) (int, error)
	flush(uintptr) error
	close(uintptr) error
	release() error
}

type journalBackend struct {
	self    *journalBackend
	mu      sync.Mutex
	plan    Plan
	root    journalRootCapability
	api     journalKernel
	file    uintptr
	seal    *seal
	raw     []byte
	record  record
	count   uint64
	history map[[32]byte]bool
	sids    map[string]bool
	// epoch は OS boot とみなさず、同 epoch の既使用 generation 以下を拒否する。
	generations                 map[[32]byte]uint64
	poisoned, uncertain, closed bool
	subjectLost                 bool
	closing                     atomic.Bool
}

func realJournalHandle(h uintptr) bool { return h != 0 && h < ^uintptr(0)-15 }
func validJournalIdentity(i journalIdentity) bool {
	return i.volume != 0 && i.fileID != ([16]byte{}) && i.owner != "" && i.security != ""
}
func newJournalBackend(ctx context.Context, p Plan, root *journalRootCapability, api journalKernel) (j *journalBackend, result error) {
	if !p.valid || root == nil || root.self != root || api == nil || !realJournalHandle(root.root) || root.installation == ([32]byte{}) || root.machine != p.binding.machine || root.service != p.binding.service || !serviceIdentity(root.serviceSID) || root.thread == 0 || !validJournalIdentity(root.rootID) || !validJournalIdentity(root.ledgerID) || root.rootID.volume != root.ledgerID.volume || root.rootID.fileID == root.ledgerID.fileID {
		return nil, ErrUnconfigured
	}
	j = &journalBackend{plan: p, root: *root, api: api, seal: &seal{4}}
	j.self = j
	defer func() {
		if recover() != nil {
			j.uncertain = true
			result = ErrUnknown
		}
		if result != nil {
			j.poisoned = true
		}
	}()
	if !finite(ctx) {
		return j, ErrUnknown
	}
	if e := j.subject(); e != nil {
		return j, ErrUnknown
	}
	r, e := api.inspect(root.root)
	if e != nil || !j.validInfo(r, true) {
		return j, ErrManual
	}
	if !finite(ctx) {
		return j, ErrUnknown
	}
	h, e := api.open(root.root)
	if h != 0 {
		j.file = h
	}
	if e != nil {
		if _, ok := e.(journalPending); ok {
			j.uncertain = true
		}
		return j, ErrUnknown
	}
	if !realJournalHandle(h) || h == root.root {
		j.uncertain = true
		return j, ErrUnknown
	}
	f, e := api.inspect(h)
	if e != nil || !j.validInfo(f, false) || f.size < journalHeaderBytes || f.size > journalHeaderBytes+journalMaxFrames*journalFrameBytes {
		return j, ErrManual
	}
	if !finite(ctx) {
		return j, ErrUnknown
	}
	if e = api.lock(h); e != nil {
		return j, ErrUnknown
	}
	data := make([]byte, int(f.size))
	if !finite(ctx) {
		return j, ErrUnknown
	}
	n, e := api.read(h, data, 0)
	if e != nil || n != len(data) || !finite(ctx) {
		return j, ErrUnknown
	}
	if e = j.replay(data); e != nil {
		return j, e
	}
	if e = j.check(ctx, j.raw); e != nil {
		return j, e
	}
	if e = api.flush(h); e != nil || !finite(ctx) {
		return j, ErrUnknown
	}
	if e = j.check(ctx, j.raw); e != nil {
		return j, e
	}
	return j, nil
}
func (j *journalBackend) validInfo(i journalFileInfo, root bool) bool {
	want := j.root.ledgerID
	if root {
		want = j.root.rootID
	}
	return i.identity == want && i.kind == 1 && i.directory == root && !i.reparse && !i.inherit && i.protected && (root || i.links == 1)
}
func (j *journalBackend) check(ctx context.Context, expected []byte) error {
	if !finite(ctx) || j.closing.Load() {
		return ErrUnknown
	}
	if e := j.subject(); e != nil {
		return ErrUnknown
	}
	r, e := j.api.inspect(j.root.root)
	if e != nil || !j.validInfo(r, true) {
		return ErrManual
	}
	if !finite(ctx) {
		return ErrUnknown
	}
	f, e := j.api.inspect(j.file)
	if e != nil || !j.validInfo(f, false) || f.size != uint64(len(expected)) {
		return ErrManual
	}
	if !finite(ctx) {
		return ErrUnknown
	}
	actual := make([]byte, len(expected))
	n, e := j.api.read(j.file, actual, 0)
	if e != nil || n != len(actual) || !bytes.Equal(actual, expected) || !finite(ctx) || j.closing.Load() {
		return ErrUnknown
	}
	if e = j.subject(); e != nil || !finite(ctx) || j.closing.Load() {
		return ErrUnknown
	}
	return nil
}

func (j *journalBackend) subject() error {
	if j.subjectLost {
		return ErrUnknown
	}
	if e := j.api.subject(j.root); e != nil {
		j.subjectLost = true
		return ErrUnknown
	}
	return nil
}
func (j *journalBackend) enter(ctx context.Context) error {
	if j == nil || j.self != j {
		return ErrUnconfigured
	}
	if !finite(ctx) {
		return ErrUnknown
	}
	if !j.mu.TryLock() {
		return ErrBusy
	}
	if j.closed || j.closing.Load() || j.poisoned || j.file == 0 {
		j.mu.Unlock()
		return ErrUnknown
	}
	return nil
}
func (j *journalBackend) finish(result *error) {
	if recover() != nil {
		j.uncertain = true
		j.poisoned = true
		*result = ErrUnknown
	}
	j.mu.Unlock()
}
func (j *journalBackend) reserve(ctx context.Context, r record) (receipt journalReceipt, result error) {
	if e := j.enter(ctx); e != nil {
		return receipt, e
	}
	defer j.finish(&result)
	if r.stage != reserved || r.binding != j.plan.binding || !validJournalRecord(r) || j.count != 0 && !journalTerminal(j.record.stage) || j.history[r.binding.intent] || r.binding.generation <= j.generations[r.binding.epoch] || j.count+journalIntentFrames > journalMaxFrames {
		return receipt, knownFailure{}
	}
	return j.append(ctx, r)
}
func (j *journalBackend) save(ctx context.Context, r record) (receipt journalReceipt, result error) {
	if e := j.enter(ctx); e != nil {
		return receipt, e
	}
	defer j.finish(&result)
	if j.count == 0 || !journalSameBase(r.binding, j.plan.binding) || !validJournalRecord(r) {
		return receipt, knownFailure{}
	}
	if r == j.record {
		if e := j.check(ctx, j.raw); e != nil {
			j.poisoned = true
			return receipt, e
		}
		return journalReceipt{j.seal, r}, nil
	}
	if !journalEdge(j.record, r) || r.stage == ownedSID && j.sids[r.binding.sid] {
		return receipt, knownFailure{}
	}
	return j.append(ctx, r)
}
func (j *journalBackend) append(ctx context.Context, r record) (journalReceipt, error) {
	if j.count >= journalMaxFrames {
		return journalReceipt{}, knownFailure{}
	}
	if e := j.check(ctx, j.raw); e != nil {
		j.poisoned = true
		return journalReceipt{}, e
	}
	prior := j.raw
	if j.count != 0 {
		prior = prior[len(prior)-journalFrameBytes:]
	}
	frame, e := journalFrame(j.count+1, sha256.Sum256(prior), r)
	if e != nil {
		return journalReceipt{}, e
	}
	if !finite(ctx) {
		j.poisoned = true
		return journalReceipt{}, ErrUnknown
	}
	n, e := j.api.write(j.file, frame, uint64(len(j.raw)))
	if e != nil || n != len(frame) || !finite(ctx) || j.closing.Load() {
		j.poisoned = true
		return journalReceipt{}, ErrUnknown
	}
	if e = j.api.flush(j.file); e != nil || !finite(ctx) || j.closing.Load() {
		j.poisoned = true
		return journalReceipt{}, ErrUnknown
	}
	next := append(append([]byte(nil), j.raw...), frame...)
	if e = j.check(ctx, next); e != nil {
		j.poisoned = true
		return journalReceipt{}, e
	}
	if e = j.replay(next); e != nil {
		j.poisoned = true
		return journalReceipt{}, e
	}
	if !finite(ctx) || j.closing.Load() {
		j.poisoned = true
		return journalReceipt{}, ErrUnknown
	}
	return journalReceipt{j.seal, r}, nil
}
func (j *journalBackend) load(ctx context.Context) (receipt journalReceipt, result error) {
	if e := j.enter(ctx); e != nil {
		return receipt, e
	}
	defer j.finish(&result)
	if e := j.check(ctx, j.raw); e != nil {
		j.poisoned = true
		return receipt, e
	}
	if j.count == 0 {
		return receipt, ErrManual
	}
	return journalReceipt{j.seal, j.record}, nil
}

// Close は既所有 file のみ閉じる。borrowed root と未知参照は保持する。
func (j *journalBackend) close(ctx context.Context) (result error) {
	if j == nil || j.self != j {
		return ErrUnconfigured
	}
	j.closing.Store(true)
	if !finite(ctx) {
		return ErrUnknown
	}
	if !j.mu.TryLock() {
		return ErrUnknown
	}
	defer j.finish(&result)
	if j.uncertain {
		return ErrUnknown
	}
	if j.file != 0 {
		if !realJournalHandle(j.file) || j.file == j.root.root {
			return ErrUnknown
		}
		if e := j.subject(); e != nil {
			return ErrUnknown
		}
		if !finite(ctx) {
			return ErrUnknown
		}
		if e := j.api.close(j.file); e != nil {
			return ErrUnknown
		}
		j.file = 0
	}
	if j.uncertain {
		return ErrUnknown
	}
	// file がなくても subject/query の部分所有参照が kernel に残り得る。
	if e := j.api.release(); e != nil {
		return ErrUnknown
	}
	if !finite(ctx) {
		return ErrUnknown
	}
	j.closed = true
	return nil
}
