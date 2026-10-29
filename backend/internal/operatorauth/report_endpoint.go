package operatorauth

import (
	"context"
	"errors"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// The caller owns h until validation succeeds. Failed adoption never closes h.
// Adoption clears HANDLE_FLAG_INHERIT only after ownership moves to the endpoint.
type messageEndpoint struct {
	o        reportOwner
	role     endpointRole
	consumed bool
}

func newNativeMessageEndpoint(h uintptr, role endpointRole, cleanup time.Duration) (*messageEndpoint, error) {
	k, e := nativeReportKernel()
	if e != nil {
		return nil, e
	}
	return newMessageEndpoint(h, role, cleanup, k)
}
func newMessageEndpoint(h uintptr, role endpointRole, cleanup time.Duration, k reportKernel) (*messageEndpoint, error) {
	if !reportHandle(h) || cleanup <= 0 || k == nil || (role != endpointServerRead && role != endpointClientWrite) {
		return nil, errReportInvalid
	}
	if e := k.validate(h, role); e != nil {
		return nil, e
	}
	endpoint := &messageEndpoint{o: reportOwner{api: k, budget: cleanup}, role: role}
	if role == endpointServerRead {
		endpoint.o.read = h
	} else {
		endpoint.o.write = h
	}
	if err := k.clearInherit(h); err != nil {
		endpoint.o.closed = true
		return endpoint, errors.Join(ErrUnconfirmed, err)
	}
	return endpoint, nil
}
func (e *messageEndpoint) reserve(ctx context.Context, role endpointRole) (uintptr, error) {
	if e == nil {
		return 0, errReportInvalid
	}
	e.o.mu.Lock()
	defer e.o.mu.Unlock()
	if e.o.closed || e.consumed || e.role != role {
		return 0, ErrDenied
	}
	e.consumed = true
	if !reportContextValid(ctx) {
		return 0, errReportInvalid
	}
	e.o.busy++
	if role == endpointServerRead {
		return e.o.read, nil
	}
	return e.o.write, nil
}
func (e *messageEndpoint) read(ctx context.Context) ([]byte, error) {
	h, err := e.reserve(ctx, endpointServerRead)
	if err != nil {
		return nil, err
	}
	b := make([]byte, servicecontrol.FrameBytes)
	n, err := e.o.run(ctx, reportRead, h, b)
	e.o.mu.Lock()
	closed := e.o.closed
	e.o.mu.Unlock()
	if closed || !reportContextValid(ctx) || err != nil || n == 0 || n > uint32(len(b)) || b[n-1] != '\n' {
		return nil, errors.Join(errReportFraming, err)
	}
	// No look-ahead: impersonation must bind to this last message.
	return append([]byte(nil), b[:n]...), nil
}
func (e *messageEndpoint) write(ctx context.Context, b []byte) error {
	h, err := e.reserve(ctx, endpointClientWrite)
	if err != nil {
		return err
	}
	if len(b) == 0 || len(b) > servicecontrol.FrameBytes || b[len(b)-1] != '\n' {
		e.o.mu.Lock()
		e.o.busy--
		e.o.mu.Unlock()
		return errors.Join(errReportFraming, e.close(ctx))
	}
	record := append([]byte(nil), b...)
	n, err := e.o.run(ctx, reportWrite, h, record)
	e.o.mu.Lock()
	closed := e.o.closed
	e.o.mu.Unlock()
	closeErr := e.close(ctx)
	if closed || !reportContextValid(ctx) || err != nil || n != uint32(len(record)) {
		return errors.Join(errReportFraming, err, closeErr)
	}
	return closeErr
}
func (e *messageEndpoint) close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	return e.o.close(ctx)
}
