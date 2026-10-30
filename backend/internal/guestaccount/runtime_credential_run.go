package guestaccount

import "context"

// This exchange deliberately does not call execute(request): secret bytes never
// enter its generic request copy, stdout/data fields, JSON, argv or environment.
func (o *sidRunOwner) executeCredential(ctx context.Context, b binding, secret []uint16) (result error) {
	if o == nil || o.self != o {
		return ErrUnconfigured
	}
	o.mu.Lock()
	if o.used || o.closing.Load() {
		o.mu.Unlock()
		return ErrDenied
	}
	o.used = true
	o.mu.Unlock()
	defer func() {
		if recover() != nil {
			o.mu.Lock()
			o.uncertain = true
			o.mu.Unlock()
			result = ErrUnknown
		}
		if result != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), o.config.spec.CleanupTimeout)
			defer cancel()
			_ = o.Close(cleanup)
		}
	}()
	if !o.live(ctx) || len(secret) != 37 {
		return ErrDenied
	}
	request := runtimeCredentialRequest(b)
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	o.mu.Lock()
	o.cancel = cancel
	setup := o.setup
	o.mu.Unlock()
	if setup == nil {
		return ErrUnconfigured
	}
	if !sidRunJoin(call, setup) || setup.err != nil || !o.live(call) {
		return ErrUnknown
	}
	results := make(chan *sidRunWork, 4)
	o.mu.Lock()
	in, out, stderr, child := o.input, o.output, o.stderr, o.child
	for i := 0; i < 4; i++ {
		o.tasks = append(o.tasks, newSIDRunWork())
	}
	tasks := o.tasks
	o.wait = tasks[3]
	o.mu.Unlock()
	go o.run(tasks[0], false, results, func() ([]byte, error) { return nil, writeRuntimeCredential(call, o, in, request, secret) })
	go o.run(tasks[1], false, results, func() ([]byte, error) { return sidRunRead(call, o, out, true) })
	go o.run(tasks[2], false, results, func() ([]byte, error) { return sidRunRead(call, o, stderr, false) })
	go o.run(tasks[3], false, results, func() ([]byte, error) { return nil, child.Wait(call) })
	for range tasks {
		select {
		case work := <-results:
			if work.err != nil || !o.live(call) {
				return ErrUnknown
			}
		case <-call.Done():
			return ErrUnknown
		}
	}
	for _, work := range tasks {
		if !sidRunJoin(call, work) {
			return ErrUnknown
		}
	}
	if !validSIDResult(request, tasks[1].data) || !o.live(call) {
		return ErrUnknown
	}
	if err := o.cleanupHeld(call); err != nil || !o.live(call) {
		return ErrCleanup
	}
	return nil
}

type runtimeCredentialExecutor struct{ owner *sidRunOwner }

func (c *runtimeCredentialExecutor) prepare(ctx context.Context, guard func() bool) error {
	if c == nil || c.owner == nil || guard == nil || !guard() || !finite(ctx) {
		return ErrDenied
	}
	o := c.owner
	o.mu.Lock()
	o.setup = newSIDRunWork()
	setup := o.setup
	o.mu.Unlock()
	go o.run(setup, true, nil, func() ([]byte, error) { return nil, o.prepare(ctx) })
	if !sidRunJoin(ctx, setup) || setup.err != nil || !guard() {
		return ErrUnknown
	}
	if _, ok := o.input.(runtimeSecretWriter); !ok {
		return ErrUnconfigured
	}
	return nil
}
func (c *runtimeCredentialExecutor) provision(ctx context.Context, b binding, s []uint16, guard func() bool) error {
	if c == nil || c.owner == nil || guard == nil || !guard() || !finite(ctx) {
		return ErrDenied
	}
	err := c.owner.executeCredential(ctx, b, s)
	if !guard() {
		return ErrUnknown
	}
	return err
}
func (c *runtimeCredentialExecutor) Close(ctx context.Context) error {
	if c == nil || c.owner == nil {
		return nil // pure constructor rejection acquired no helper or pipe
	}
	return c.owner.Close(ctx)
}
