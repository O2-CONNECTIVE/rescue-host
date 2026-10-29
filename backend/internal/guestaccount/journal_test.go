package guestaccount

import (
	"encoding/binary"
	"testing"
)

func journalPlan(t *testing.T, n, generation uint64) Plan {
	t.Helper()
	base := testPlan(t)
	var intent [32]byte
	binary.LittleEndian.PutUint64(intent[:8], n)
	p, e := NewPlan(ProtectedInputs{sessionEpoch: base.binding.epoch, intentID: intent, generation: generation, machineSID: base.binding.machine, operatorSID: base.binding.operator, serviceUserSID: base.binding.service, policy: base.policy})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func reopenJournal(t *testing.T, j *journalBackend, k *journalKernelFixture, p Plan) (*journalBackend, error) {
	t.Helper()
	root := j.root
	root.self = &root
	return newJournalBackend(testContext(t), p, &root, k)
}

func TestJournalRejectsReusedSessionGenerationWithNewIntent(t *testing.T) {
	j, k, _, _ := journalFixture(t)
	if _, e := j.reserve(testContext(t), record{j.plan.binding, reserved}); e != nil {
		t.Fatal(e)
	}
	if _, e := j.save(testContext(t), record{j.plan.binding, abortComplete}); e != nil {
		t.Fatal(e)
	}
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	p := journalPlan(t, 3, 1)
	next, e := reopenJournal(t, j, k, p)
	if e != nil {
		t.Fatal(e)
	}
	writes := k.writes
	if _, e = next.reserve(testContext(t), record{p.binding, reserved}); e == nil || k.writes != writes {
		t.Fatal("same epoch/generation reused with new intent", e)
	}
}

type journalKernelFixture struct {
	data                           []byte
	root                           journalRootCapability
	openHandle                     uintptr
	writes, flushes, closes, opens int
	writeErr, flushErr, closeErr   error
	shortWrite                     bool
	releaseErr                     error
	active                         bool
	openErr, readErr, subjectErr   error
	shortRead                      bool
	onWrite, onFlush, onClose      func()
	onInspect                      func(*journalFileInfo)
}

func (k *journalKernelFixture) subject(journalRootCapability) error { return k.subjectErr }
func (k *journalKernelFixture) release() error                      { return k.releaseErr }
func (k *journalKernelFixture) inspect(h uintptr) (journalFileInfo, error) {
	if h == k.root.root {
		return journalFileInfo{identity: k.root.rootID, kind: 1, links: 1, directory: true, protected: true}, nil
	}
	i := journalFileInfo{identity: k.root.ledgerID, size: uint64(len(k.data)), kind: 1, links: 1, protected: true}
	if k.onInspect != nil {
		k.onInspect(&i)
	}
	return i, nil
}
func (k *journalKernelFixture) open(uintptr) (uintptr, error) {
	k.opens++
	if k.active {
		return 0, ErrBusy
	}
	if k.openHandle != 0 {
		k.active = true
	}
	return k.openHandle, k.openErr
}
func (k *journalKernelFixture) lock(uintptr) error { return nil }
func (k *journalKernelFixture) read(_ uintptr, b []byte, off uint64) (int, error) {
	if off > uint64(len(k.data)) {
		return 0, ErrUnknown
	}
	n := copy(b, k.data[off:])
	if k.shortRead && n > 0 {
		n--
	}
	return n, k.readErr
}
func (k *journalKernelFixture) write(_ uintptr, b []byte, off uint64) (int, error) {
	k.writes++
	if k.onWrite != nil {
		k.onWrite()
	}
	if off != uint64(len(k.data)) {
		return 0, ErrUnknown
	}
	n := len(b)
	if k.shortWrite {
		n--
	}
	k.data = append(k.data, b[:n]...)
	return n, k.writeErr
}
func (k *journalKernelFixture) flush(uintptr) error {
	k.flushes++
	if k.onFlush != nil {
		k.onFlush()
	}
	return k.flushErr
}
func (k *journalKernelFixture) close(uintptr) error {
	k.closes++
	if k.onClose != nil {
		k.onClose()
	}
	if k.closeErr == nil {
		k.active = false
	}
	return k.closeErr
}

func journalFixture(t *testing.T) (*journalBackend, *journalKernelFixture, *Lifecycle, *fixture) {
	t.Helper()
	p := testPlan(t)
	root := &journalRootCapability{root: 11, installation: [32]byte{9}, machine: p.binding.machine, service: p.binding.service, serviceSID: "S-1-5-80-1-2-3-4-5", thread: 7, rootID: journalIdentity{volume: 1, fileID: [16]byte{1}, owner: p.binding.service, security: "protected-root"}, ledgerID: journalIdentity{volume: 1, fileID: [16]byte{2}, owner: p.binding.service, security: "protected-ledger"}}
	root.self = root
	k := &journalKernelFixture{root: *root, openHandle: 12, data: journalHeader(root.installation)}
	j, err := newJournalBackend(testContext(t), p, root, k)
	if err != nil {
		t.Fatal(err)
	}
	_, f := newFixture(t)
	f.s.journal = j
	f.s.journalSeal = j.seal
	l, err := newLifecycle(p, f.s)
	if err != nil {
		t.Fatal(err)
	}
	return j, k, l, f
}

func TestJournalLifecycleCommitsBeforeAccountAndClosesTerminal(t *testing.T) {
	j, k, l, f := journalFixture(t)
	f.createHook = func() {
		if k.writes < 2 || k.flushes < 3 {
			t.Error("account side effect preceded durable createAttempt")
		}
	}
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	r, err := j.load(testContext(t))
	if err != nil || r.record.stage != deleteComplete || r.record.binding.sid == "" {
		t.Fatal("journal did not retain full terminal binding", r, err)
	}
	writes := k.writes
	if _, err = j.save(testContext(t), r.record); err != nil || k.writes != writes {
		t.Fatal("same terminal save was not idempotent", err)
	}
}
