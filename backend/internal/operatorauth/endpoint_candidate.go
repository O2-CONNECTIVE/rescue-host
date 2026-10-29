package operatorauth

import (
	"context"
	"errors"
	"sync"
)

// このleaseはBatchが所有する同じread端から候補を取得する。認可や正規policy
// の発行を行わず、入力handleを借用する。Serviceの端点所有移動は別の責務。
type endpointCandidateKernel interface {
	subject() error
	endpoint(uintptr) (uint32, error)
	openProcess(uint32) (uintptr, error)
	openToken(uintptr) (uintptr, error)
	queryKernel() profileQueryKernel
	close(uintptr) error
}
type endpointCandidateLease struct {
	mu                                           sync.Mutex
	input, process, token                        uintptr
	api                                          endpointCandidateKernel
	query                                        *profileQuery
	pid                                          uint32
	started, accepted, failed, closed, uncertain bool
	fatal                                        error
}

func newEndpointCandidateLease(input uintptr, api endpointCandidateKernel) (*endpointCandidateLease, error) {
	if !helperHandle(input) || api == nil {
		return nil, ErrUnconfigured
	}
	return &endpointCandidateLease{input: input, api: api}, nil
}

// typed SoleInput の Move 後だけ、同じ Batch の実 return を一度結ぶ。
// 既存 lease を再発行したり、既取得候補を別 input に付け替えたりしない。
func (l *endpointCandidateLease) bindInput(input uintptr) error {
	if l == nil || !l.mu.TryLock() {
		return ErrDenied
	}
	defer l.mu.Unlock()
	if !helperHandle(input) || l.input != 0 || l.api == nil || l.started || l.closed || l.failed || l.uncertain {
		return ErrDenied
	}
	l.input = input
	return nil
}

// 同じBatch inputとlaunch相関へpure構成する。policyの正規発行は別境界。
func endpointProfileSpec(s nativeBatchSpec, policy profileProviderPolicy, api endpointCandidateKernel) (nativeBatchSpec, error) {
	if s.producer != nil || policy.material.launchID != s.launch.launchID || policy.material.cleanup != s.launch.cleanup {
		return nativeBatchSpec{}, ErrUnconfigured
	}
	lease, err := newEndpointCandidateLease(s.input, api)
	if err != nil {
		return nativeBatchSpec{}, err
	}
	provider, err := newProtectedProfileProvider(policy, lease)
	if err != nil {
		return nativeBatchSpec{}, err
	}
	s.producer = provider
	return s, nil
}
func (l *endpointCandidateLease) inspect(ctx context.Context) (out profileCandidateSnapshot, err error) {
	if l == nil || !l.mu.TryLock() {
		return out, ErrDenied
	}
	defer l.mu.Unlock()
	defer func() {
		if recover() != nil {
			l.uncertain = true
			err = ErrUnconfirmed
		}
		if err != nil {
			l.failed = true
			out = profileCandidateSnapshot{}
		}
		if errors.Is(err, ErrFatal) {
			l.fatal = errors.Join(ErrFatal, err)
		}
	}()
	if l.fatal != nil {
		return out, l.fatal
	}
	if l.failed || l.closed || !helperHandle(l.input) || !reportContextValid(ctx) {
		return out, ErrDenied
	}
	l.started = true
	if err = l.api.subject(); err != nil {
		return out, err
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	if !l.accepted {
		if l.pid, err = l.api.endpoint(l.input); err != nil || l.pid == 0 {
			return out, errors.Join(ErrDenied, err)
		}
		if !reportContextValid(ctx) {
			return out, ErrDenied
		}
		process, e := l.api.openProcess(l.pid)
		if helperHandle(process) && process != l.input {
			l.process = process
		}
		if e != nil || l.process == 0 {
			return out, errors.Join(ErrDenied, e)
		}
		if !reportContextValid(ctx) {
			return out, ErrDenied
		}
		token, e := l.api.openToken(l.process)
		if helperHandle(token) && token != l.input && token != l.process {
			l.token = token
		}
		if e != nil || l.token == 0 {
			return out, errors.Join(ErrDenied, e)
		}
		if !reportContextValid(ctx) {
			return out, ErrDenied
		}
		l.query, err = newProfileQuery(l.process, l.token, l.api.queryKernel())
		if err != nil {
			return out, err
		}
	}
	out, err = l.query.inspect(ctx)
	if err != nil {
		return out, err
	}
	if out.pid != l.pid {
		return out, ErrDenied
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	if !l.accepted {
		// 取得前後を同じ未移譲read端で照合。取得後は保持process/tokenだけを
		// 再検査し、Batchが閉じた番号を後でqueryしない。
		pid, e := l.api.endpoint(l.input)
		if e != nil || pid != l.pid {
			return out, errors.Join(ErrDenied, e)
		}
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	if err = l.api.subject(); err != nil {
		return out, err
	}
	if !reportContextValid(ctx) {
		return out, ErrDenied
	}
	l.accepted = true
	return out, nil
}
func (l *endpointCandidateLease) close(ctx context.Context) (err error) {
	if l == nil {
		return nil
	}
	if !l.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer l.mu.Unlock()
	defer func() {
		if recover() != nil {
			l.uncertain = true
			err = ErrUnconfirmed
		}
		if errors.Is(err, ErrFatal) {
			l.fatal = errors.Join(ErrFatal, err)
		}
	}()
	l.closed = true
	if l.fatal != nil {
		return l.fatal
	}
	if !l.started {
		return nil
	}
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	if err = l.api.subject(); err != nil {
		return err
	}
	if l.query != nil {
		if err = l.query.close(ctx); err != nil {
			return err
		}
	}
	for _, ref := range []*uintptr{&l.token, &l.process} {
		if *ref == 0 {
			continue
		}
		if !reportContextValid(ctx) {
			return ErrUnconfirmed
		}
		if err = l.api.close(*ref); err != nil {
			return errors.Join(ErrUnconfirmed, err)
		}
		*ref = 0
	}
	if l.uncertain || !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
