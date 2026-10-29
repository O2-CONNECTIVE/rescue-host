package serviceready

import (
	"context"
	"errors"
)

const endpointReadAccess uint32 = 0x120089
const endpointWriteAccess uint32 = 0x2

// SoleInput is a single destructive ownership transfer, not a borrowed handle
// getter or authentication proof. It must remain on Endpoint's worker thread.
type SoleInput struct {
	self     *SoleInput
	endpoint *Endpoint
}

// Split duplicates exact noninheritable read-only and minimal write handles from
// the actual duplex, checks their rights, then closes the original duplex before
// returning an input. Partial acquisitions stay with Endpoint; failure is sticky.
func (e *Endpoint) Split(ctx context.Context) (input *SoleInput, err error) {
	if !e.canonical() {
		return nil, ErrDenied
	}
	defer func() {
		if err != nil {
			e.fail(err)
			input = nil
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
	if !e.connected || e.split || e.duplex == nil {
		return nil, ErrDenied
	}
	e.split = true
	if err = e.inspectDuplex(); err != nil {
		return nil, err
	}
	if err = e.check(ctx); err != nil {
		return nil, err
	}
	e.read, err = e.duplex.duplicate(endpointReadAccess)
	if err != nil || e.read == nil {
		return nil, errors.Join(ErrDenied, err)
	}
	if err = e.read.validate(endpointReadAccess); err != nil {
		return nil, err
	}
	if err = e.check(ctx); err != nil {
		return nil, err
	}
	e.writer, err = e.duplex.duplicate(endpointWriteAccess)
	if err != nil || e.writer == nil {
		return nil, errors.Join(ErrDenied, err)
	}
	if err = e.writer.validate(endpointWriteAccess); err != nil {
		return nil, err
	}
	if err = e.check(ctx); err != nil {
		return nil, err
	}
	if err = e.inspectDuplex(); err != nil {
		return nil, err
	}
	if err = e.duplex.close(); err != nil {
		return nil, err
	}
	e.duplex = nil
	if err = e.check(ctx); err != nil {
		return nil, err
	}
	input = &SoleInput{endpoint: e}
	input.self = input
	e.input = input
	return input, nil
}

// Move transfers the owned read handle exactly once on the preparing worker.
// A nonzero result belongs to the recipient even when accompanied by an error;
// the recipient must record and close it. Endpoint never closes moved handles.
// Batch must close every parent read copy before helper resume; that lifecycle
// and its Factory/profile binding are subsequent, unconnected composition work.
func (s *SoleInput) Move(ctx context.Context) (owned uintptr, err error) {
	if s == nil || s.self != s || !s.endpoint.canonical() {
		return 0, ErrDenied
	}
	e := s.endpoint
	defer func() {
		if err != nil {
			e.fail(err)
		}
	}()
	if err = e.enter(ctx); err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			e.fail(err)
		}
		e.query.gate.Unlock()
	}()
	if e.input != s || e.moved || e.read == nil || e.duplex != nil {
		return 0, ErrDenied
	}
	if err = e.read.validate(endpointReadAccess); err != nil {
		return 0, err
	}
	if err = e.check(ctx); err != nil {
		return 0, err
	}
	owned, err = e.read.take()
	if owned != 0 {
		e.read = nil
		e.moved = true
	}
	if err != nil {
		return owned, err
	}
	if owned == 0 || owned >= ^uintptr(15) {
		return owned, ErrDenied
	}
	return owned, e.check(ctx)
}
