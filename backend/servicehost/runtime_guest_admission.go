package servicehost

import (
	"context"
	"sync"
)

// These watchers observe cancellation only. They never produce native cleanup
// evidence; the outer owner's actual Close reply still owns that acknowledgement.
type runtimeGuestWatch struct {
	stop func() bool
	done chan struct{}
	once sync.Once
}

func newRuntimeGuestWatch(ctx context.Context, action func()) *runtimeGuestWatch {
	w := &runtimeGuestWatch{done: make(chan struct{})}
	w.stop = context.AfterFunc(ctx, func() { defer w.finish(); action() })
	return w
}
func (w *runtimeGuestWatch) finish() { w.once.Do(func() { close(w.done) }) }
func (w *runtimeGuestWatch) stopWatching() bool {
	if w.stop() {
		w.finish()
		return true
	}
	return false // cancellation won or its callback already started; never detach as success
}
func (w *runtimeGuestWatch) join(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
}

func (a *runtimeGuestActor) releaseAdmission(ctx context.Context) error {
	if a.admissionWatch != nil {
		a.admissionWatch.stopWatching()
	}
	a.cancelAdmission()
	if a.admissionWatch != nil && a.admissionWatch.join(ctx) != nil {
		return ErrRuntimeUnconfirmed
	}
	if r := a.record; r != nil && r.requestWatch != nil {
		r.requestWatch.stopWatching()
		return r.requestWatch.join(ctx)
	}
	return nil
}
