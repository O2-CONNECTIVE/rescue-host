package operatorauth

import (
	"context"
	"errors"
)

// Batch is a concrete retained native owner. A future protected provider must
// prepare it privately and register it with Service before Start. There is no
// public factory; the zero value is unconfigured and cannot acquire resources.
type Batch = nativeBatch

// Collector returns the exact collector created during preparation. It does not
// authenticate a Read or replace the collector's issuer checks.
func (b *nativeBatch) Collector() *Collector {
	if b == nil || b.self != b {
		return nil
	}
	return b.collector
}

func (b *nativeBatch) revoke() error {
	b.mu.Lock()
	b.closing = true
	b.collector.mu.Lock()
	b.collector.failed, b.collector.closed = true, true
	b.collector.mu.Unlock()
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		startCancel, collectCancel := b.startCancel, b.collectCancel
		b.mu.Unlock()
		b.collector.mu.Lock()
		activeCancel := b.collector.activeCancel
		b.collector.mu.Unlock()
		if startCancel != nil {
			startCancel()
		}
		if collectCancel != nil {
			collectCancel()
		}
		if activeCancel != nil {
			activeCancel()
		}
	}()
	b.revokeOnce.Do(func() {
		b.revokeErr = ErrUnconfirmed
		b.revokeErr = b.revokeOwner()
	})
	return b.revokeErr
}

func (b *nativeBatch) awaitStart(ctx context.Context) error {
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	b.mu.Lock()
	starting := b.starting
	b.mu.Unlock()
	if starting {
		select {
		case <-b.startDone:
		case <-ctx.Done():
			return ErrUnconfirmed
		}
	}
	return nil
}

func (b *nativeBatch) admitted(ctx context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closing && reportContextValid(ctx) && reportContextValid(b.request)
}

func (b *nativeBatch) acquireOperation(ctx context.Context) bool {
	if !finite(ctx) {
		return false
	}
	select {
	case b.operation <- struct{}{}:
		if !finite(ctx) {
			<-b.operation
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func (b *nativeBatch) Collect(ctx context.Context) (*Read, error) {
	if b == nil || b.self != b || b.collector == nil || b.operation == nil {
		return nil, ErrUnconfigured
	}
	if !finite(ctx) {
		return nil, ErrDenied
	}
	if !b.acquireOperation(ctx) {
		return nil, ErrDenied
	}
	defer func() { <-b.operation }()
	b.mu.Lock()
	if !b.ready || b.closing {
		b.mu.Unlock()
		return nil, ErrDenied
	}
	ctx, cancel := context.WithCancel(ctx)
	b.collectCancel = cancel
	b.mu.Unlock()

	r, err := b.collector.Collect(ctx)
	if err != nil {
		cancel()
	}
	b.mu.Lock()
	fatal := b.fatal
	closing := b.closing
	b.mu.Unlock()
	if fatal != nil {
		return nil, errors.Join(err, ErrFatal, fatal)
	}
	if closing {
		return nil, ErrDenied
	}
	return r, err
}

// Close stops admission and revokes the same issuer before cancellation. It
// cannot confirm cleanup until the in-flight constructor returns all references
// and acknowledges completion. Timeout retains this Batch and its Collector.
func (b *nativeBatch) Close(ctx context.Context) (result error) {
	if b == nil || b.self != b || b.collector == nil || b.operation == nil {
		return ErrUnconfigured
	}
	defer func() {
		if recover() != nil {
			result = ErrUnconfirmed
		}
	}()
	b.mu.Lock()
	b.closing = true
	startCancel, collectCancel, starting := b.startCancel, b.collectCancel, b.starting
	b.mu.Unlock()
	_ = b.revoke()
	if startCancel != nil {
		startCancel()
	}
	if collectCancel != nil {
		collectCancel()
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	if starting {
		select {
		case <-b.startDone:
		case <-ctx.Done():
			return ErrUnconfirmed
		}
	}
	if !b.acquireOperation(ctx) {
		return ErrUnconfirmed
	}
	defer func() { <-b.operation }()
	return b.collector.Close(ctx)
}
