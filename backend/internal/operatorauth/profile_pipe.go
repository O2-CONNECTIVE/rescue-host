package operatorauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// This interface is supplied only by the protected Service owner. Neither its
// presence nor the material's launch ID constitutes authentication. Production
// has no configured producer until that owner supplies verified token evidence.
type profilePipeProducer interface {
	prepare(context.Context) (profilePipeMaterial, error)
}

type profilePipeMaterial struct {
	launchID string
	profile  helperProfile
	cleanup  time.Duration
}

type profilePipeKernel interface {
	create() (uintptr, uintptr, error)
	inheritReader(uintptr) (uintptr, error)
	close(uintptr) error
}

type profilePipeEndpoint struct {
	handle    uintptr
	closing   chan error
	uncertain bool
}

type profilePipe struct {
	mu                         sync.Mutex
	kernel                     profilePipeKernel
	api                        profileSyncAPI
	parentRead, reader, writer profilePipeEndpoint
	frame                      []byte
	expected                   string
	cleanup                    time.Duration
	worker                     *profileSyncOwner
	attempted, stopped         bool
	acquisitionUncertain       bool
}

func newProfilePipe(ctx context.Context, producer profilePipeProducer, kernel profilePipeKernel, api profileSyncAPI) (*profilePipe, error) {
	m, frame, err := prepareProfilePipe(ctx, producer)
	if err != nil {
		return nil, err
	}
	if kernel == nil || api == nil {
		return nil, ErrUnconfigured
	}
	return newPreparedProfilePipe(ctx, m, frame, kernel, api)
}

func prepareProfilePipe(ctx context.Context, producer profilePipeProducer) (profilePipeMaterial, []byte, error) {
	if producer == nil {
		return profilePipeMaterial{}, nil, ErrUnconfigured
	}
	if !reportContextValid(ctx) {
		return profilePipeMaterial{}, nil, ErrDenied
	}
	m, err := producer.prepare(ctx)
	if err != nil {
		return profilePipeMaterial{}, nil, err
	}
	frame, err := encodeHelperProfileFrame(m.launchID, m.profile)
	if err != nil || m.cleanup <= 0 || !reportContextValid(ctx) {
		return profilePipeMaterial{}, nil, ErrDenied
	}
	return m, frame, nil
}

func newPreparedProfilePipe(ctx context.Context, material profilePipeMaterial, frame []byte, kernel profilePipeKernel, api profileSyncAPI) (p *profilePipe, err error) {
	p = &profilePipe{kernel: kernel, api: api, frame: frame, expected: material.launchID, cleanup: material.cleanup}
	defer func() {
		if recover() != nil {
			p.acquisitionUncertain = true
			err = ErrUnconfirmed
		}
		if err != nil {
			p.stopped = true
		}
	}()
	r, w, e := kernel.create()
	if reportHandle(r) {
		p.parentRead.handle = r
	}
	if reportHandle(w) && w != r {
		p.writer.handle = w
	}
	if e != nil || !reportHandle(r) || !reportHandle(w) || r == w {
		return p, errors.Join(ErrDenied, e)
	}
	if !reportContextValid(ctx) {
		return p, ErrDenied
	}
	reader, e := kernel.inheritReader(r)
	if reportHandle(reader) && reader != r && reader != w {
		p.reader.handle = reader
	}
	if e != nil || p.reader.handle == 0 {
		return p, errors.Join(ErrDenied, e)
	}
	if e = p.closeEndpoint(ctx, &p.parentRead); e != nil {
		return p, e
	}
	return p, nil
}

// launchID is a correlation value copied from the Service's producer; it is
// never accepted as authentication or proof of a same-launch OS binding.
func (p *profilePipe) launchID() string {
	if p == nil {
		return ""
	}
	return p.expected
}

func (p *profilePipe) childReader() uintptr {
	if p == nil || !p.mu.TryLock() {
		return 0
	}
	defer p.mu.Unlock()
	if p.stopped || p.attempted {
		return 0
	}
	return p.reader.handle
}

func (p *profilePipe) releaseReader(ctx context.Context) error {
	if p == nil || !p.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer p.mu.Unlock()
	if p.stopped || p.attempted {
		return ErrDenied
	}
	return p.closeEndpoint(ctx, &p.reader)
}

// Only nativeBatch calls this after resume and its parent/child rechecks. A
// released handle or a launch correlation string alone is not resume evidence.
func (p *profilePipe) sendAfterResume(ctx context.Context) error {
	if p == nil || !p.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer p.mu.Unlock()
	if p.stopped || p.attempted || p.reader.handle != 0 || p.parentRead.handle != 0 || !reportContextValid(ctx) {
		return ErrDenied
	}
	p.attempted = true
	worker, err := startProfileSync(ctx, p.api, profileSyncWrite, p.writer.handle, p.frame)
	p.worker = worker
	if err != nil {
		return err
	}
	n, err := worker.wait(ctx, p.cleanup, false)
	if err != nil {
		return err
	}
	if err = worker.release(); err != nil {
		return err
	}
	p.worker = nil
	if int(n) != len(p.frame) {
		return ErrDenied
	}
	if err = p.closeEndpoint(ctx, &p.writer); err != nil {
		return err
	}
	p.frame = nil
	return nil
}

// A deadline, CancelSynchronousIo success, or a cancellation error never permits
// endpoint close. Only the sync owner's actual return and joined release do.
func (p *profilePipe) close(ctx context.Context) error {
	if p == nil || !p.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer p.mu.Unlock()
	p.stopped = true
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	if p.worker != nil {
		_, err := p.worker.wait(ctx, p.cleanup, true)
		if releaseErr := p.worker.release(); releaseErr != nil {
			return errors.Join(ErrUnconfirmed, err, releaseErr)
		}
		p.worker = nil
	}
	for _, endpoint := range []*profilePipeEndpoint{&p.parentRead, &p.reader, &p.writer} {
		if err := p.closeEndpoint(ctx, endpoint); err != nil {
			return err
		}
	}
	if p.acquisitionUncertain {
		return ErrUnconfirmed
	}
	p.frame = nil
	return nil
}

func (p *profilePipe) closeEndpoint(ctx context.Context, endpoint *profilePipeEndpoint) error {
	if endpoint.handle == 0 {
		return nil
	}
	if !reportContextValid(ctx) || endpoint.uncertain {
		return ErrUnconfirmed
	}
	if endpoint.closing == nil {
		result := make(chan error, 1)
		endpoint.closing = result
		handle := endpoint.handle
		go func() {
			var err error
			defer func() {
				if recover() != nil {
					err = ErrUnconfirmed
				}
				result <- err
			}()
			err = p.kernel.close(handle)
		}()
	}
	select {
	case err := <-endpoint.closing:
		endpoint.closing = nil
		if err != nil {
			if errors.Is(err, ErrUnconfirmed) {
				endpoint.uncertain = true
			}
			return errors.Join(ErrUnconfirmed, err)
		}
		endpoint.handle = 0
		return nil
	case <-ctx.Done():
		return errors.Join(ErrUnconfirmed, ctx.Err())
	}
}
