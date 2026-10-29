package operatorauth

import (
	"context"
	"errors"
)

func (p *reportPair) childWriter() uintptr {
	if p == nil {
		return 0
	}
	p.o.mu.Lock()
	defer p.o.mu.Unlock()
	if p.o.closed {
		return 0
	}
	return p.o.child
}
func (p *reportPair) releaseWriter(ctx context.Context) error {
	if p == nil {
		return errReportInvalid
	}
	c, cancel, e := boundedCleanup(ctx, p.o.budget)
	if e != nil {
		return e
	}
	defer cancel()
	if e = p.o.admitCleanup(c); e != nil {
		return e
	}
	defer p.o.cleanup.Unlock()
	p.o.mu.Lock()
	unsafe := p.o.busy != 0 || p.o.unresolved()
	p.o.mu.Unlock()
	if unsafe {
		return ErrUnconfirmed
	}
	return errors.Join(p.o.closeHandle(c, &p.o.child), p.o.closeHandle(c, &p.o.write))
}
func (p *reportPair) drain(ctx context.Context) (reportResult, error) {
	if p == nil {
		return reportResult{}, errReportInvalid
	}
	p.o.mu.Lock()
	if p.consumed || p.o.closed {
		p.o.mu.Unlock()
		return reportResult{}, ErrDenied
	}
	p.consumed = true
	if !reportContextValid(ctx) || p.o.child != 0 || p.o.write != 0 {
		p.o.mu.Unlock()
		return reportResult{}, ErrDenied
	}
	p.o.busy++
	h := p.o.read
	p.o.mu.Unlock()
	b := make([]byte, authenticatedReportBytes)
	n, e := p.o.run(ctx, reportRead, h, b)
	if e != nil || n == 0 || n > uint32(len(b)) || b[n-1] != '\n' {
		return reportResult{}, errors.Join(errReportFraming, e)
	}
	p.o.mu.Lock()
	if p.o.closed {
		p.o.mu.Unlock()
		return reportResult{}, errReportFraming
	}
	p.o.busy++
	p.o.mu.Unlock()
	n2, e := p.o.run(ctx, reportRead, h, make([]byte, 1))
	p.o.mu.Lock()
	closed := p.o.closed
	p.o.mu.Unlock()
	if closed || !reportContextValid(ctx) || n2 != 0 || !errors.Is(e, errReportBrokenPipe) || errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return reportResult{}, errors.Join(errReportFraming, e)
	}
	return reportResult{frame: append([]byte(nil), b[:n]...), eof: true}, nil
}
func (p *reportPair) close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.o.close(ctx)
}
