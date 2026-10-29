package guestaccount

import (
	"context"
	"io"
)

type sidRunWork struct {
	done chan struct{}
	data []byte
	err  error
}

func newSIDRunWork() *sidRunWork { return &sidRunWork{done: make(chan struct{})} }
func sidRunDone(w *sidRunWork) bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}
func sidRunJoin(ctx context.Context, w *sidRunWork) bool {
	if !finite(ctx) {
		return false
	}
	select {
	case <-w.done:
		return finite(ctx)
	case <-ctx.Done():
		return false
	}
}
func (o *sidRunOwner) run(w *sidRunWork, uncertain bool, results chan<- *sidRunWork, fn func() ([]byte, error)) {
	defer func() {
		if recover() != nil {
			w.data = nil
			w.err = ErrUnknown
			if uncertain {
				o.mu.Lock()
				o.uncertain = true
				o.mu.Unlock()
			}
		}
		if results != nil {
			results <- w
		}
		close(w.done)
	}()
	w.data, w.err = fn()
}

func sidRunWrite(ctx context.Context, o *sidRunOwner, w io.WriteCloser, request []byte) error {
	if !o.live(ctx) {
		return ErrUnknown
	}
	n, e := w.Write(request)
	if e != nil || n != len(request) || !o.live(ctx) {
		return ErrUnknown
	}
	if e = w.Close(); e != nil || !o.live(ctx) {
		return ErrUnknown
	}
	return nil
}
func sidRunRead(ctx context.Context, o *sidRunOwner, r io.Reader, keep bool) ([]byte, error) {
	var out []byte
	total := 0
	for {
		if !o.live(ctx) {
			return nil, ErrUnknown
		}
		size := 4096
		if remaining := sidRunByteLimit + 1 - total; remaining < size {
			size = remaining
		}
		buffer := make([]byte, size)
		n, e := r.Read(buffer)
		if n < 0 || n > len(buffer) || !o.live(ctx) {
			return nil, ErrUnknown
		}
		total += n
		if total > sidRunByteLimit {
			return nil, ErrUnknown
		}
		if keep {
			out = append(out, buffer[:n]...)
		}
		if e != nil {
			if e == io.EOF {
				return out, nil
			}
			return nil, ErrUnknown
		}
		if n == 0 {
			return nil, ErrUnknown
		}
	}
}
