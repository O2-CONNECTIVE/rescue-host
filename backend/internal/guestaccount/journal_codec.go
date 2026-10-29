package guestaccount

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

func journalHeader(id [32]byte) []byte {
	b := make([]byte, journalHeaderBytes)
	copy(b, "RHJGNL01")
	binary.LittleEndian.PutUint32(b[8:12], 1)
	copy(b[16:48], id[:])
	h := sha256.Sum256(b[:48])
	copy(b[48:], h[:])
	return b
}
func validJournalRecord(r record) bool {
	b := r.binding
	h := sha256.Sum256(b.intent[:])
	if b.epoch == ([32]byte{}) || b.intent == ([32]byte{}) || b.policy == ([32]byte{}) || b.generation == 0 || !machineSID(b.machine) || !accountSID(b.operator) || !serviceIdentity(b.service) || b.name != "rhg_"+hex.EncodeToString(h[:8]) {
		return false
	}
	if r.stage == reserved || r.stage == createAttempt || r.stage == abortComplete {
		return b.sid == ""
	}
	return r.stage >= ownedSID && r.stage <= deleteComplete && accountSID(b.sid) && strings.HasPrefix(b.sid, b.machine+"-") && b.sid != b.operator && b.sid != b.service
}
func journalSameBase(a, b binding) bool { a.sid = ""; b.sid = ""; return a == b }
func journalTerminal(s stage) bool      { return s == deleteComplete || s == abortComplete }
func journalEdge(a, b record) bool {
	if !validJournalRecord(b) || !journalSameBase(a.binding, b.binding) {
		return false
	}
	if a.stage == createAttempt && b.stage == ownedSID {
		return a.binding.sid == "" && b.binding.sid != ""
	}
	if a.binding != b.binding {
		return false
	}
	switch a.stage {
	case reserved:
		return b.stage == createAttempt || b.stage == abortComplete
	case ownedSID:
		return b.stage == preparedDisabled || b.stage == deleteAttempt
	case preparedDisabled:
		return b.stage == deleteAttempt
	case deleteAttempt:
		return b.stage == deleteComplete
	}
	return false
}
func journalFrame(seq uint64, previous [32]byte, r record) ([]byte, error) {
	if seq == 0 || !validJournalRecord(r) {
		return nil, ErrManual
	}
	b := make([]byte, journalFrameBytes)
	binary.LittleEndian.PutUint64(b[:8], seq)
	copy(b[8:40], previous[:])
	b[40] = byte(r.stage)
	copy(b[48:80], r.binding.epoch[:])
	copy(b[80:112], r.binding.intent[:])
	copy(b[112:144], r.binding.policy[:])
	binary.LittleEndian.PutUint64(b[144:152], r.binding.generation)
	off := 152
	for _, s := range []string{r.binding.machine, r.binding.operator, r.binding.service, r.binding.name, r.binding.sid} {
		if len(s) > 184 {
			return nil, ErrManual
		}
		binary.LittleEndian.PutUint16(b[off:off+2], uint16(len(s)))
		off += 2
		copy(b[off:], s)
		off += len(s)
	}
	h := sha256.Sum256(b[:journalFrameBytes-32])
	copy(b[journalFrameBytes-32:], h[:])
	return b, nil
}
func decodeJournalFrame(b []byte, seq uint64, previous [32]byte) (record, error) {
	var r record
	if len(b) != journalFrameBytes || binary.LittleEndian.Uint64(b[:8]) != seq || !bytes.Equal(b[8:40], previous[:]) {
		return r, ErrManual
	}
	r.stage = stage(b[40])
	copy(r.binding.epoch[:], b[48:80])
	copy(r.binding.intent[:], b[80:112])
	copy(r.binding.policy[:], b[112:144])
	r.binding.generation = binary.LittleEndian.Uint64(b[144:152])
	off := 152
	values := []*string{&r.binding.machine, &r.binding.operator, &r.binding.service, &r.binding.name, &r.binding.sid}
	for _, value := range values {
		n := int(binary.LittleEndian.Uint16(b[off : off+2]))
		off += 2
		if n > 184 || off+n > journalFrameBytes-32 {
			return record{}, ErrManual
		}
		*value = string(b[off : off+n])
		off += n
	}
	expected, e := journalFrame(seq, previous, r)
	if e != nil || !bytes.Equal(expected, b) {
		return record{}, ErrManual
	}
	return r, nil
}

func (j *journalBackend) replay(data []byte) error {
	if len(data) < journalHeaderBytes || len(data) > journalHeaderBytes+journalMaxFrames*journalFrameBytes || !bytes.Equal(data[:journalHeaderBytes], journalHeader(j.root.installation)) || (len(data)-journalHeaderBytes)%journalFrameBytes != 0 {
		return ErrManual
	}
	j.history = map[[32]byte]bool{}
	j.sids = map[string]bool{}
	j.generations = map[[32]byte]uint64{}
	previous := sha256.Sum256(data[:journalHeaderBytes])
	var current record
	for off := journalHeaderBytes; off < len(data); off += journalFrameBytes {
		seq := uint64((off-journalHeaderBytes)/journalFrameBytes + 1)
		r, e := decodeJournalFrame(data[off:off+journalFrameBytes], seq, previous)
		if e != nil {
			return e
		}
		if r.binding.machine != j.root.machine || r.binding.service != j.root.service {
			return ErrManual
		}
		if r.stage == reserved {
			if seq != 1 && !journalTerminal(current.stage) || j.history[r.binding.intent] || r.binding.generation <= j.generations[r.binding.epoch] || seq-1+journalIntentFrames > journalMaxFrames {
				return ErrManual
			}
			j.history[r.binding.intent] = true
			j.generations[r.binding.epoch] = r.binding.generation
		} else if seq == 1 || !journalEdge(current, r) {
			return ErrManual
		}
		if current.binding.sid == "" && r.binding.sid != "" {
			if j.sids[r.binding.sid] {
				return ErrManual
			}
			j.sids[r.binding.sid] = true
		}
		current = r
		previous = sha256.Sum256(data[off : off+journalFrameBytes])
	}
	j.count = uint64((len(data) - journalHeaderBytes) / journalFrameBytes)
	j.record = current
	j.raw = append([]byte(nil), data...)
	return nil
}
