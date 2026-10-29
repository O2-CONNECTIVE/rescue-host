package operatorauth

import (
	"context"
	"errors"
	"runtime"
	"time"
)

type helperEntryOwner struct {
	lease          helperEntryLease
	config         helperEntryConfig
	factory        helperEntryFactory
	raw            [3]uintptr
	unknown        [3]bool
	reader, writer *messageEndpoint
	receiver       *profileReceiver
}

func (o *helperEntryOwner) adoptMessage(slot int, role endpointRole) (result error) {
	defer func() {
		if recover() != nil {
			o.unknown[slot] = true
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
	}()
	endpoint, err := o.factory.message(o.raw[slot], role, o.config.cleanup)
	if endpoint != nil {
		o.raw[slot] = 0
		if slot == 0 {
			o.reader = endpoint
		} else {
			o.writer = endpoint
		}
	}
	if err != nil || endpoint == nil {
		return errors.Join(ErrDenied, err)
	}
	return nil
}
func (o *helperEntryOwner) adoptProfile(ctx context.Context, source profileReceiverSource) (result error) {
	defer func() {
		if recover() != nil {
			o.unknown[2] = true
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
	}()
	var err error
	o.receiver, err = o.factory.profile(ctx, o.raw[2], o.config.binding, source, o.config.cleanup)
	if o.receiver != nil {
		o.raw[2] = 0
	}
	if err != nil || o.receiver == nil {
		return errors.Join(ErrDenied, err)
	}
	return nil
}

// Native endpoint close may outlive its cancellation budget. Keep its original
// owner captured by the one worker and report unknown completion on timeout.
// Lease evidence is released only after all handle ownership is confirmed.
func helperEntryClose(ctx context.Context, closeFn func() error) error {
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		var err error
		defer func() {
			if recover() != nil {
				err = ErrUnconfirmed
			} else {
				runtime.UnlockOSThread()
			}
			done <- err
		}()
		err = closeFn()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errors.Join(ErrUnconfirmed, ctx.Err())
	}
}
func (o *helperEntryOwner) close() (result error) {
	defer func() {
		if recover() != nil {
			result = errors.Join(result, ErrFatal, ErrUnconfirmed)
		}
	}()
	c := o.config
	if !reportHandle(c.operatorRead) || !reportHandle(c.reportWrite) || !reportHandle(c.binding.profileRead) ||
		c.operatorRead == c.reportWrite || c.operatorRead == c.binding.profileRead || c.reportWrite == c.binding.profileRead {
		result = ErrUnconfirmed
	}
	budget := o.config.cleanup
	if budget <= 0 || budget > time.Minute {
		return ErrUnconfirmed
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	for _, closeFn := range []func() error{
		func() error { return o.receiver.close(ctx) },
		func() error { return o.writer.close(ctx) },
		func() error { return o.reader.close(ctx) },
	} {
		if err := helperEntryClose(ctx, closeFn); err != nil {
			result = errors.Join(result, ErrUnconfirmed, err)
		}
	}
	seen := map[uintptr]bool{}
	for i, handle := range o.raw {
		if handle == 0 || seen[handle] {
			continue
		}
		seen[handle] = true
		if o.unknown[i] || !reportHandle(handle) || o.factory.closeRaw == nil {
			result = errors.Join(result, ErrUnconfirmed)
			continue
		}
		if err := helperEntryClose(ctx, func() error { return o.factory.closeRaw(handle) }); err != nil {
			result = errors.Join(result, ErrUnconfirmed, err)
		} else {
			o.raw[i] = 0
		}
	}
	if result != nil {
		return result
	}
	if err := o.lease.close(ctx); err != nil || !reportContextValid(ctx) {
		return errors.Join(ErrUnconfirmed, err)
	}
	return nil
}
