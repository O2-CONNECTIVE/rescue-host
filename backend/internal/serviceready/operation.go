package serviceready

import "sync"

type operation interface {
	poll() (bool, error)
	cancel() error
	release() error
}
type ownedOperation struct {
	mu                        sync.Mutex
	raw                       operation
	done, released, cancelled bool
	err                       error
}

func (o *ownedOperation) tryStep(cancel bool) (bool, error) {
	if !o.mu.TryLock() {
		return false, ErrUnconfirmed
	}
	defer o.mu.Unlock()
	if cancel && !o.done && !o.cancelled {
		o.cancelled = true
		_ = o.raw.cancel()
	}
	if !o.done {
		o.done, o.err = o.raw.poll()
	}
	if !o.done {
		return false, o.err
	}
	if !o.released {
		if err := o.raw.release(); err != nil {
			return false, err
		}
		o.released = true
	}
	return true, o.err
}
