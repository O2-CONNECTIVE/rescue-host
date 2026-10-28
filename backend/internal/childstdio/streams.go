package childstdio

import (
	"context"
	"errors"
	"io"
	"time"
)

const chunkBytes = 64 * 1024

type input struct{ e *endpoint }
type output struct{ e *endpoint }

func (e *endpoint) isClosed() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.closed }
func (e *endpoint) take() error {
	select {
	case <-e.stop:
		return io.ErrClosedPipe
	case <-e.serial:
	}
	if e.isClosed() {
		e.serial <- struct{}{}
		return io.ErrClosedPipe
	}
	return nil
}

// runは呼出元sliceを受け取らない。bufferはopの所有下に残り得る。
func (e *endpoint) run(kind ioKind, buffer []byte) (uint32, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	e.busy = true
	h := e.parent
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.busy = false; e.mu.Unlock() }()
	raw, err := e.api.start(kind, h, buffer)
	if raw == nil {
		if err == nil {
			err = ErrConfig
		}
		return 0, err
	}
	op := &ownedOperation{raw: raw}
	e.mu.Lock()
	e.op = op
	e.mu.Unlock()
	if err != nil {
		e.signal()
		_, _, _ = op.tryStep(true)
		return 0, errors.Join(ErrUnconfirmed, err)
	}
	for {
		cancelled := e.isClosed()
		done, n, err := op.tryStep(cancelled)
		if done {
			if cancelled || e.isClosed() {
				return 0, io.ErrClosedPipe
			}
			if n > uint32(len(buffer)) {
				e.signal()
				return 0, ErrUnconfirmed
			}
			return n, err
		}
		if cancelled {
			return 0, errors.Join(io.ErrClosedPipe, ErrUnconfirmed, err)
		}
		if err != nil {
			e.signal()
			_, _, _ = op.tryStep(true)
			return 0, errors.Join(ErrUnconfirmed, err)
		}
		// idle時は期限を作らず、Closeの通知だけで通常待機を打ち切る。
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-e.stop:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (w *input) Write(b []byte) (int, error) {
	if err := w.e.take(); err != nil {
		return 0, err
	}
	defer func() { w.e.serial <- struct{}{} }()
	written := 0
	for len(b) > 0 {
		size := len(b)
		if size > chunkBytes {
			size = chunkBytes
		}
		owned := append([]byte(nil), b[:size]...)
		n, err := w.e.run(writeIO, owned)
		written += int(n)
		if err != nil {
			return written, err
		}
		if int(n) != size {
			return written, io.ErrShortWrite
		}
		b = b[size:]
	}
	return written, nil
}
func (r *output) Read(b []byte) (int, error) {
	if err := r.e.take(); err != nil {
		return 0, err
	}
	defer func() { r.e.serial <- struct{}{} }()
	if len(b) == 0 {
		return 0, nil
	}
	size := len(b)
	if size > chunkBytes {
		size = chunkBytes
	}
	owned := make([]byte, size)
	n, err := r.e.run(readIO, owned)
	if err != nil {
		if errors.Is(err, errBrokenPipe) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && n == 0 && !r.e.isClosed() {
			return 0, io.EOF
		}
		return 0, err
	}
	copy(b, owned[:n])
	return int(n), nil
}
func (w *input) Close() error  { return w.e.closeStream() }
func (r *output) Close() error { return r.e.closeStream() }
func (e *endpoint) closeStream() error {
	e.signal()
	// io.Closerにはcallerの残予算を渡せないため待機を作らない。
	// op/cleanupのGo mutex admissionも一度のTryLockだけで確認する。
	if !e.cleanup.TryLock() {
		return ErrUnconfirmed
	}
	defer e.cleanup.Unlock()
	e.mu.Lock()
	op, busy := e.op, e.busy
	e.mu.Unlock()
	complete := op == nil
	if op != nil {
		complete, _, _ = op.tryStep(true)
	}
	if !complete || busy {
		return ErrUnconfirmed
	}
	return errors.Join(e.closeHandle(&e.original), e.closeHandle(&e.child), e.closeHandle(&e.parent))
}
