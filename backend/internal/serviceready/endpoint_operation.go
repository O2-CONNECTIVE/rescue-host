package serviceready

import (
	"context"
	"errors"
)

type endpointWriteOperation interface {
	poll() (bool, uint32, error)
	cancel() error
	release() error
}
type endpointOwnedWrite struct {
	raw                      endpointWriteOperation
	done, released, canceled bool
	n                        uint32
	err                      error
}

// All steps are serialized by the same Endpoint query gate. Terminal error and
// release are independent; unknown query/release never frees buffer or writer.
func (o *endpointOwnedWrite) step(cancel bool) (bool, uint32, error) {
	if cancel && !o.done && !o.canceled {
		o.canceled = o.raw.cancel() == nil
	}
	if !o.done {
		o.done, o.n, o.err = o.raw.poll()
	}
	if !o.done {
		return false, o.n, o.err
	}
	if !o.released {
		if err := o.raw.release(); err != nil {
			return false, o.n, err
		}
		o.released = true
	}
	return true, o.n, o.err
}

// WriteResponse performs one 1..16384-byte physical response write after the
// destructive input Move. It copies the frame, never retries a partial message,
// and issues a receipt only after all bytes, actual terminal status and release.
// This phase order is not helper authentication or client receipt evidence.
func (e *Endpoint) WriteResponse(ctx context.Context, frame []byte) (receipt *WriteReceipt, err error) {
	if !e.canonical() {
		return nil, ErrDenied
	}
	defer func() {
		if err != nil {
			e.fail(err)
			receipt = nil
		}
	}()
	if err = e.enter(ctx); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			e.fail(err)
		}
		e.query.gate.Unlock()
	}()
	if !e.connected || !e.split || !e.moved || e.read != nil || e.duplex != nil || e.writer == nil || e.writeStarted || len(frame) == 0 || len(frame) > 16384 {
		return nil, ErrDenied
	}
	e.writeStarted = true
	if err = e.writer.validate(endpointWriteAccess); err != nil {
		return nil, err
	}
	if err = e.check(ctx); err != nil {
		return nil, err
	}
	buffer := append([]byte(nil), frame...)
	raw, err := e.writer.startWrite(buffer)
	if raw != nil {
		e.write = &endpointOwnedWrite{raw: raw}
	}
	if err != nil || raw == nil {
		return nil, errors.Join(ErrDenied, err)
	}
	for {
		done, n, stepErr := e.write.step(false)
		if stepErr != nil {
			return nil, stepErr
		}
		if err = e.check(ctx); err != nil {
			return nil, err
		}
		if done {
			if n != uint32(len(buffer)) {
				return nil, ErrDenied
			}
			receipt = &WriteReceipt{endpoint: e}
			receipt.self = receipt
			e.written.Store(receipt)
			return receipt, nil
		}
		pause(ctx)
	}
}
