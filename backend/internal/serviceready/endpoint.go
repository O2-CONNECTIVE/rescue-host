package serviceready

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type endpointHandle interface {
	validate(uint32) error
	take() (uintptr, error)
	close() error
	startWrite([]byte) (endpointWriteOperation, error)
}
type endpointDuplex interface {
	listenerRef
	duplicate(uint32) (endpointHandle, error)
}
type endpointKernel interface {
	createEndpoint(string, string) (endpointDuplex, error)
}

// Endpoint owns one physical pipe on its preparing worker's fixed thread.
// It provides no operator authentication or Service readiness proof. The native
// Factory/profile binding to Batch remains unconnected. Do not copy this owner.
type Endpoint struct {
	self             *Endpoint
	query            *Plane
	duplex           endpointDuplex
	connection       *ownedOperation
	connected        bool
	split, moved     bool
	read, writer     endpointHandle
	input            *SoleInput
	write            *endpointOwnedWrite
	writeStarted     bool
	written          atomic.Pointer[WriteReceipt]
	failed, closing  atomic.Bool
	closed           atomic.Pointer[EndpointClosed]
	inspection       *InspectionLease // q.gate owns issuance; the lease owns its duplicate.
	inspectionActive atomic.Int32
}

// WriteReceipt only records this Endpoint's successful physical response write.
// It does not prove client receipt, authentication, or Batch completion.
type WriteReceipt struct {
	self     *WriteReceipt
	endpoint *Endpoint
}

// EndpointClosed acknowledges only this Endpoint's operations, query references
// and retained handles. It cannot prove closure of readers moved to Batch/helper.
type EndpointClosed struct {
	self     *EndpointClosed
	endpoint *Endpoint
}

// PrepareEndpoint owns the existing query-only prefix on this worker, then
// creates one physical pipe. A nonnil partial owner must be closed even on error.
func (f *Factory) PrepareEndpoint(ctx context.Context) (e *Endpoint, err error) {
	if f == nil || !f.configured {
		return nil, ErrConfig
	}
	if !serviceWitnessLive(ctx) {
		return nil, ErrDenied
	}
	q, err := f.prepareQuery(ctx)
	if q == nil {
		return nil, err
	}
	e = &Endpoint{query: q}
	e.self = e
	defer func() {
		if err != nil {
			e.fail(err)
		}
	}()
	if err != nil {
		return e, err
	}
	q.prepared = true
	if !serviceWitnessLive(ctx) {
		return e, ErrDenied
	}
	api, ok := q.api.(endpointKernel)
	if !ok {
		return e, ErrUnavailable
	}
	e.duplex, err = api.createEndpoint(q.c.Endpoint, listenerSDDL(q.c.Profile))
	if err != nil || e.duplex == nil {
		return e, errors.Join(ErrDenied, err)
	}
	if err = e.check(ctx); err != nil {
		return e, err
	}
	if err = e.inspectDuplex(); err != nil {
		return e, err
	}
	if err = e.check(ctx); err != nil {
		return e, err
	}
	raw, err := e.duplex.connect()
	if raw != nil {
		e.connection = &ownedOperation{raw: raw}
	}
	if err != nil || raw == nil {
		return e, errors.Join(ErrDenied, err)
	}
	return e, e.check(ctx)
}

func (e *Endpoint) canonical() bool { return e != nil && e.self == e && e.query != nil }
func (e *Endpoint) fail(err error) error {
	e.failed.Store(true)
	return e.query.invalidate(err)
}
func (e *Endpoint) check(ctx context.Context) error {
	q := e.query
	if q.poisoned.Load() {
		return ErrFatal
	}
	if !serviceWitnessLive(ctx) || !q.prepared || e.failed.Load() || e.closing.Load() || q.revoked.Load() || q.stopped.Load() {
		return ErrDenied
	}
	if _, err := q.verify(ctx, false); err != nil {
		return err
	}
	if q.poisoned.Load() {
		return ErrFatal
	}
	if !serviceWitnessLive(ctx) || e.failed.Load() || e.closing.Load() || q.revoked.Load() || q.stopped.Load() {
		return ErrDenied
	}
	return nil
}
func (e *Endpoint) enter(ctx context.Context) error {
	if e.query.poisoned.Load() {
		return ErrFatal
	}
	if !serviceWitnessLive(ctx) || !admit(ctx, &e.query.gate) {
		return ErrDenied
	}
	if err := e.check(ctx); err != nil {
		e.query.gate.Unlock()
		return err
	}
	return nil
}
func (e *Endpoint) inspectDuplex() error {
	l, err := e.duplex.inspect()
	q := e.query
	if err != nil {
		return err
	}
	if l.kind != 3 || l.flags != 5 || l.mode != 2 || l.maxInstances != 1 || l.inBytes != 16384 || l.outBytes != 16384 || l.serverPID != q.pid || l.handleFlags != 0 || !sameSecurity(l.security, listenerSecurity(q.c.Profile)) {
		return ErrDenied
	}
	return nil
}

// AwaitConnected requires actual connect terminal status and OVERLAPPED/event
// release before requerying the actual duplex pipe. No peer is authenticated.
func (e *Endpoint) AwaitConnected(ctx context.Context) (err error) {
	if !e.canonical() {
		return ErrDenied
	}
	defer func() {
		if err != nil {
			e.fail(err)
		}
	}()
	if err = e.enter(ctx); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			e.fail(err)
		}
		e.query.gate.Unlock()
	}()
	if e.connected || e.connection == nil || e.duplex == nil {
		return ErrDenied
	}
	for {
		done, stepErr := e.connection.tryStep(false)
		if stepErr != nil {
			return stepErr
		}
		if err = e.check(ctx); err != nil {
			return err
		}
		if done {
			if err = e.inspectDuplex(); err != nil {
				return err
			}
			if err = e.check(ctx); err != nil {
				return err
			}
			e.connected = true
			return nil
		}
		pause(ctx)
	}
}

// CloseEndpoint stops admission before finite cleanup. Unknown operations retain
// their pipe and OVERLAPPED/event on this owner for retry. A moved reader is owned
// by its recipient; this acknowledgement never includes Batch/helper readers.
func (e *Endpoint) CloseEndpoint(ctx context.Context) (receipt *EndpointClosed, err error) {
	if !e.canonical() {
		return nil, errors.Join(ErrDenied, ErrUnconfirmed)
	}
	q := e.query
	defer func() {
		if err != nil {
			e.failed.Store(true)
		}
	}()
	e.closing.Store(true)
	q.revoked.Store(true)
	if q.api != nil && q.thread != 0 && q.api.threadID() != q.thread {
		q.poisoned.Store(true)
		return nil, q.closeResult(ErrUnconfirmed)
	}
	if !finite(ctx) {
		return nil, q.closeResult(errors.Join(ErrConfig, ErrUnconfirmed))
	}
	deadline, _ := ctx.Deadline()
	if end := time.Now().Add(q.c.CleanupTimeout); end.Before(deadline) {
		deadline = end
	}
	cleanup, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if !admit(cleanup, &q.gate) {
		return nil, q.closeResult(ErrUnconfirmed)
	}
	if e.inspectionActive.Load() != 0 {
		q.gate.Unlock()
		return nil, q.closeResult(ErrUnconfirmed)
	}
	var result error
	complete := e.connection == nil
	for !complete {
		done, err := e.connection.tryStep(true)
		complete = done
		if errors.Is(err, ErrFatal) {
			q.poisoned.Store(true)
		}
		if complete {
			break
		}
		if !serviceWitnessLive(cleanup) {
			result = errors.Join(ErrUnconfirmed, err)
			break
		}
		pause(cleanup)
	}
	if complete && e.duplex != nil {
		if err := e.duplex.close(); err != nil {
			result = errors.Join(result, ErrUnconfirmed, err)
		} else {
			e.duplex = nil
		}
	}
	writeComplete := e.write == nil
	for !writeComplete {
		done, _, stepErr := e.write.step(true)
		writeComplete = done
		if errors.Is(stepErr, ErrFatal) {
			q.poisoned.Store(true)
		}
		if writeComplete {
			break
		}
		if !serviceWitnessLive(cleanup) {
			result = errors.Join(result, ErrUnconfirmed, stepErr)
			break
		}
		pause(cleanup)
	}
	refs := []*endpointHandle{&e.read}
	if writeComplete {
		refs = append(refs, &e.writer)
	}
	for _, ref := range refs {
		if *ref != nil {
			if err := (*ref).close(); err != nil {
				result = errors.Join(result, ErrUnconfirmed, err)
			} else {
				*ref = nil
			}
		}
	}
	q.gate.Unlock()
	result = errors.Join(result, q.Close(cleanup))
	if result != nil {
		return nil, q.closeResult(result)
	}
	if !serviceWitnessLive(cleanup) {
		return nil, q.closeResult(ErrUnconfirmed)
	}
	if !admit(cleanup, &q.gate) {
		return nil, q.closeResult(ErrUnconfirmed)
	}
	defer q.gate.Unlock()
	if !serviceWitnessLive(cleanup) {
		return nil, q.closeResult(ErrUnconfirmed)
	}
	if e.closed.Load() == nil {
		closed := &EndpointClosed{endpoint: e}
		closed.self = closed
		e.closed.Store(closed)
	}
	return e.closed.Load(), nil
}

// MatchesCompletion only matches this physical owner's exact opaque receipts.
// It is not authentication or full exchange completion. Service must separately
// require actual Batch.Collect/Consume, r.done and ownerDone before next admission.
func (e *Endpoint) MatchesCompletion(write *WriteReceipt, closed *EndpointClosed) bool {
	return e.canonical() && !e.failed.Load() && !e.query.poisoned.Load() && write != nil && write.self == write && write.endpoint == e && e.written.Load() == write && closed != nil && closed.self == closed && closed.endpoint == e && e.closed.Load() == closed
}
