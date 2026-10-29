package operatorauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

var (
	errReportInvalid    = errors.New("invalid report I/O")
	errReportFraming    = errors.New("invalid report message or EOF")
	errReportBrokenPipe = errors.New("actual report broken pipe")
	errReportMoreData   = errors.New("report message exceeds buffer")
)

type reportKind uint8

const (
	reportConnect reportKind = iota
	reportRead
	reportWrite
)

type endpointRole uint8

const (
	endpointServerRead endpointRole = iota + 1
	endpointClientWrite
)

type reportConfig struct {
	ownerSID, serviceSID, helperSID, guestSID string
	cleanup                                   time.Duration
}
type reportKernel interface {
	primary() (string, map[string]uint32, error)
	create(string, string) (uintptr, error)
	open(string) (uintptr, error)
	duplicate(uintptr) (uintptr, error)
	validate(uintptr, endpointRole) error
	clearInherit(uintptr) error
	close(uintptr) error
	start(reportKind, uintptr, []byte) (reportOperation, error)
}
type reportOperation interface {
	poll() (bool, uint32, error)
	cancel() error
	release() error
}

func (s reportConfig) valid() bool {
	ids := []string{s.ownerSID, s.serviceSID, s.helperSID, s.guestSID}
	for i, sid := range ids {
		if !reportSIDValid(sid) {
			return false
		}
		for _, prior := range ids[:i] {
			if sid == prior {
				return false
			}
		}
	}
	return s.cleanup > 0
}
func reportHandle(h uintptr) bool { return h != 0 && h < ^uintptr(0)-15 }

type reportPair struct {
	o        reportOwner
	consumed bool
}

func newNativeReportPair(ctx context.Context, s reportConfig) (*reportPair, error) {
	if !reportContextValid(ctx) || !s.valid() {
		return nil, errReportInvalid
	}
	k, e := nativeReportKernel()
	if e != nil {
		return nil, e
	}
	return newReportPair(ctx, s, k)
}
func newReportPair(ctx context.Context, s reportConfig, k reportKernel) (p *reportPair, err error) {
	if !reportContextValid(ctx) || !s.valid() || k == nil {
		return nil, errReportInvalid
	}
	user, groups, e := k.primary()
	if e != nil || user != s.ownerSID || groups[s.serviceSID]&4 == 0 || groups[s.serviceSID]&0x10 != 0 {
		return nil, errors.Join(ErrDenied, e)
	}
	if !reportContextValid(ctx) {
		return nil, ErrDenied
	}
	p = &reportPair{o: reportOwner{api: k, budget: s.cleanup}}
	defer func() {
		if recover() != nil {
			err = ErrUnconfirmed
			return
		}
		if err != nil {
			// cleanup自身のpanicでも同じpartial ownerを返す。
			defer func() {
				if recover() != nil {
					err = errors.Join(err, ErrUnconfirmed)
				}
			}()
			if e := p.close(ctx); e != nil {
				err = errors.Join(err, e)
			} else {
				p = nil
			}
		}
	}()
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return p, err
	}
	name := `\\.\pipe\rescue-auth-report-` + hex.EncodeToString(nonce[:])
	sd := "O:" + s.ownerSID + "D:P(D;;GA;;;" + s.guestSID + ")(A;;GA;;;" + s.ownerSID + ")(A;;GA;;;" + s.serviceSID + ")(A;;0x120102;;;" + s.helperSID + ")"
	if !reportContextValid(ctx) {
		return p, ErrDenied
	}
	p.o.read, err = k.create(name, sd)
	if !reportHandle(p.o.read) {
		p.o.read = 0
		err = errors.Join(err, errReportInvalid)
	}
	if err != nil {
		return p, err
	}
	if !reportContextValid(ctx) {
		return p, ErrDenied
	}
	raw, e := k.start(reportConnect, p.o.read, nil)
	if raw == nil {
		return p, errors.Join(errReportInvalid, e)
	}
	op := &reportOwnedIO{op: raw}
	p.o.pending = append(p.o.pending, op)
	if e != nil {
		return p, e
	}
	if !reportContextValid(ctx) {
		return p, ErrDenied
	}
	p.o.write, err = k.open(name)
	if !reportHandle(p.o.write) || p.o.write == p.o.read {
		p.o.write = 0
		err = errors.Join(err, errReportInvalid)
	}
	if err != nil {
		return p, err
	}
	if _, err = op.wait(ctx, s.cleanup, false); err != nil {
		return p, err
	}
	if !reportContextValid(ctx) {
		return p, ErrDenied
	}
	p.o.child, err = k.duplicate(p.o.write)
	if !reportHandle(p.o.child) || p.o.child == p.o.read || p.o.child == p.o.write {
		p.o.child = 0
		err = errors.Join(err, errReportInvalid)
	}
	if err != nil {
		return p, err
	}
	return p, nil
}
