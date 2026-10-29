package operatorauth

import (
	"context"
	"errors"
	"sync"
)

// process/token は正規 endpoint owner がlease期間中保持する借用参照。
// このqueryはendpointの所有・接続・ACL・排他を証明せず、参照を複製しない。
type profilePrimaryEvidence struct {
	projection          tokenProjection
	tokenID, modifiedID uint64
}
type profileQueryKernel interface {
	subject() error
	process(uintptr) (profileCandidateSnapshot, error)
	token(uintptr) (profilePrimaryEvidence, error)
	current(uintptr) (uintptr, error)
	close(uintptr) error
}
type profileQuery struct {
	mu                                 sync.Mutex
	process, token, probe, finalProbe  uintptr
	api                                profileQueryKernel
	started, closed, failed, uncertain bool
	fatal                              error
}

func newProfileQuery(process, token uintptr, api profileQueryKernel) (*profileQuery, error) {
	if !helperHandle(process) || !helperHandle(token) || process == token || api == nil {
		return nil, ErrUnconfigured
	}
	return &profileQuery{process: process, token: token, api: api}, nil
}
func (q *profileQuery) inspect(ctx context.Context) (out profileCandidateSnapshot, err error) {
	if q == nil || !q.mu.TryLock() {
		return out, ErrDenied
	}
	defer q.mu.Unlock()
	defer func() {
		if recover() != nil {
			q.uncertain = true
			err = ErrUnconfirmed
		}
		if err != nil {
			q.failed = true
			out = profileCandidateSnapshot{}
		}
		if errors.Is(err, ErrFatal) {
			q.fatal = errors.Join(ErrFatal, err)
		}
	}()
	if q.fatal != nil {
		return out, q.fatal
	}
	if q.closed || q.failed || q.probe != 0 || !reportContextValid(ctx) {
		return out, ErrDenied
	}
	q.started = true
	if err = q.api.subject(); err != nil {
		return out, err
	}
	before, e := q.api.process(q.process)
	if e != nil {
		return out, e
	}
	held, e := q.api.token(q.token)
	if e != nil {
		return out, e
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	probe, e := q.api.current(q.process)
	if helperHandle(probe) && probe != q.process && probe != q.token {
		q.probe = probe
	}
	if e != nil || q.probe == 0 {
		return out, errors.Join(ErrDenied, e)
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	current, e := q.api.token(q.probe)
	if e != nil {
		return out, e
	}
	heldAfter, e := q.api.token(q.token)
	if e != nil {
		return out, e
	}
	after, e := q.api.process(q.process)
	if e != nil {
		return out, e
	}
	if before != after || held != heldAfter || held != current || held.projection.kind != 1 || held.projection.level != 0 || held.tokenID == 0 || held.modifiedID == 0 {
		return out, ErrDenied
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	final, e := q.api.current(q.process)
	if helperHandle(final) && final != q.process && final != q.token && final != q.probe {
		q.finalProbe = final
	}
	if e != nil || q.finalProbe == 0 {
		return out, errors.Join(ErrDenied, e)
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	last, e := q.api.token(q.finalProbe)
	if e != nil || last != held {
		return out, errors.Join(ErrDenied, e)
	}
	if err = q.api.subject(); err != nil {
		return out, err
	}
	if err = q.api.close(q.finalProbe); err != nil {
		return out, errors.Join(ErrUnconfirmed, err)
	}
	q.finalProbe = 0
	if err = q.api.close(q.probe); err != nil {
		return out, errors.Join(ErrUnconfirmed, err)
	}
	q.probe = 0
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	after.primary, after.tokenID, after.modifiedID = held.projection, held.tokenID, held.modifiedID
	return after, nil
}
func (q *profileQuery) close(ctx context.Context) (err error) {
	if q == nil {
		return nil
	}
	if !q.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer q.mu.Unlock()
	defer func() {
		if recover() != nil {
			q.uncertain = true
			err = ErrUnconfirmed
		}
		if errors.Is(err, ErrFatal) {
			q.fatal = errors.Join(ErrFatal, err)
		}
	}()
	q.closed = true
	if q.fatal != nil {
		return q.fatal
	}
	if !q.started {
		return nil
	} // pure prepareだけなら所有query参照はなく、thread照会も不要。
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	if err = q.api.subject(); err != nil {
		return err
	}
	if q.finalProbe != 0 {
		if err = q.api.close(q.finalProbe); err != nil {
			return errors.Join(ErrUnconfirmed, err)
		}
		q.finalProbe = 0
	}
	if q.probe != 0 {
		if err = q.api.close(q.probe); err != nil {
			return errors.Join(ErrUnconfirmed, err)
		}
		q.probe = 0
	}
	if q.uncertain {
		return ErrUnconfirmed
	}
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
