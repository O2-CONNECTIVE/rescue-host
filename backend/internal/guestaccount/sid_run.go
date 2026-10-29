package guestaccount

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

type sidRunConfig struct {
	spec  childjob.Spec
	stdio childstdio.Setup
}
type sidRunPipes interface {
	ChildHandles() childjob.Handles
	Input() io.WriteCloser
	Output() io.ReadCloser
	Stderr() io.ReadCloser
	ReleaseChild(context.Context) error
	Close(context.Context) error
}
type sidRunChild interface {
	Wait(context.Context) error
	Close(context.Context) error
}
type sidRunOps struct {
	open  func(context.Context, childstdio.Setup) (sidRunPipes, error)
	start func(context.Context, childjob.Spec) (sidRunChild, error)
}

const sidRunByteLimit = 16 * 1024

type sidRunOwner struct {
	self                           *sidRunOwner
	config                         sidRunConfig
	ops                            sidRunOps
	mu, cleanup                    sync.Mutex
	closing                        atomic.Bool
	used, uncertain, closed        bool
	cancel                         context.CancelFunc
	setup                          *sidRunWork
	pipes                          sidRunPipes
	child                          sidRunChild
	input                          io.WriteCloser
	output, stderr                 io.ReadCloser
	tasks                          []*sidRunWork
	wait, stop, pipeClose          *sidRunWork
	childConfirmed, pipesConfirmed bool
}

// production は既存所有 API へ固定接続する。script/argv は保護側 factory が固定する。
func newNativeSIDExecutor(config sidRunConfig) (*sidRunOwner, error) {
	return newSIDRunOwner(config, sidRunOps{
		open: func(ctx context.Context, s childstdio.Setup) (sidRunPipes, error) {
			p, e := childstdio.NewNative(ctx, s)
			if p == nil {
				return nil, e
			}
			return p, e
		},
		start: func(ctx context.Context, s childjob.Spec) (sidRunChild, error) {
			c, e := childjob.Start(ctx, s)
			if c == nil {
				return nil, e
			}
			return c, e
		},
	})
}
func sidRunImage(path string) bool {
	if !utf8.ValidString(path) || len(path) < 7 || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1:3] != `:\` || !strings.HasSuffix(strings.ToLower(path), ".exe") {
		return false
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, `<>:"/|?*`) {
			return false
		}
		for _, r := range part {
			if r < 32 {
				return false
			}
		}
	}
	return true
}
func sidRunHandle(h uintptr) bool { return h != 0 && h < ^uintptr(15) }
func newSIDRunOwner(config sidRunConfig, ops sidRunOps) (*sidRunOwner, error) {
	s := config.spec
	if !sidRunImage(s.Artifact.Path) || s.Artifact.SHA256 == ([32]byte{}) || !sidRunHandle(s.PrimaryToken) || s.Command == nil || s.Bootstrap != nil || s.Handles != (childjob.Handles{}) || s.CleanupTimeout <= 0 || config.stdio.CleanupTimeout <= 0 || !serviceIdentity(config.stdio.UserSID) || !serviceIdentity(config.stdio.ServiceSID) || ops.open == nil || ops.start == nil {
		return nil, ErrUnconfigured
	}
	o := &sidRunOwner{config: config, ops: ops}
	o.self = o
	return o, nil
}
func (o *sidRunOwner) live(ctx context.Context) bool { return finite(ctx) && !o.closing.Load() }
func (o *sidRunOwner) execute(ctx context.Context, request []byte) (output []byte, result error) {
	if o == nil || o.self != o {
		return nil, ErrUnconfigured
	}
	o.mu.Lock()
	if o.used || o.closing.Load() {
		o.mu.Unlock()
		return nil, ErrUnknown
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
			output = nil
			cleanup, cancel := context.WithTimeout(context.Background(), o.config.spec.CleanupTimeout)
			defer cancel()
			if e := o.Close(cleanup); e != nil {
				result = errors.Join(ErrUnknown, ErrCleanup)
			}
		}
	}()
	if !o.live(ctx) || len(request) == 0 || len(request) > sidRunByteLimit {
		return nil, ErrUnknown
	}
	owned := append([]byte(nil), request...)
	ctx, cancel := context.WithCancel(ctx)
	o.mu.Lock()
	o.cancel = cancel
	setup := newSIDRunWork()
	o.setup = setup
	o.mu.Unlock()
	defer cancel()
	go o.run(setup, true, nil, func() ([]byte, error) { return nil, o.prepare(ctx) })
	if !sidRunJoin(ctx, setup) || setup.err != nil || !o.live(ctx) {
		return nil, ErrUnknown
	}
	results := make(chan *sidRunWork, 4)
	o.mu.Lock()
	if !o.live(ctx) {
		o.mu.Unlock()
		return nil, ErrUnknown
	}
	in, out, stderr, child := o.input, o.output, o.stderr, o.child
	for i := 0; i < 4; i++ {
		o.tasks = append(o.tasks, newSIDRunWork())
	}
	tasks := o.tasks
	o.wait = tasks[3]
	o.mu.Unlock()
	go o.run(tasks[0], false, results, func() ([]byte, error) { return nil, sidRunWrite(ctx, o, in, owned) })
	go o.run(tasks[1], false, results, func() ([]byte, error) { return sidRunRead(ctx, o, out, true) })
	go o.run(tasks[2], false, results, func() ([]byte, error) { return sidRunRead(ctx, o, stderr, false) })
	go o.run(tasks[3], false, results, func() ([]byte, error) { return nil, child.Wait(ctx) })
	for range tasks {
		select {
		case w := <-results:
			if w.err != nil || !o.live(ctx) {
				return nil, ErrUnknown
			}
		case <-ctx.Done():
			return nil, ErrUnknown
		}
	}
	for _, w := range tasks {
		if !sidRunJoin(ctx, w) {
			return nil, ErrUnknown
		}
	}
	if len(tasks[1].data) == 0 {
		return nil, ErrUnknown
	}
	if !o.live(ctx) {
		return nil, ErrUnknown
	}
	if e := o.cleanupHeld(ctx); e != nil || !o.live(ctx) {
		return nil, ErrUnknown
	}
	return append([]byte(nil), tasks[1].data...), nil
}

func (o *sidRunOwner) prepare(ctx context.Context) error {
	if !o.live(ctx) {
		return ErrUnknown
	}
	p, e := o.ops.open(ctx, o.config.stdio)
	if p != nil {
		o.mu.Lock()
		o.pipes = p
		o.mu.Unlock()
	}
	if e != nil || p == nil || !o.live(ctx) {
		return ErrUnknown
	}
	h := p.ChildHandles()
	if !sidRunHandle(h.Stdin) || !sidRunHandle(h.Stdout) || !sidRunHandle(h.Stderr) || h.Stdin == h.Stdout || h.Stdin == h.Stderr || h.Stdout == h.Stderr || h.Stdin == o.config.spec.PrimaryToken || h.Stdout == o.config.spec.PrimaryToken || h.Stderr == o.config.spec.PrimaryToken {
		return ErrUnknown
	}
	if !o.live(ctx) {
		return ErrUnknown
	}
	spec := o.config.spec
	spec.Handles = h
	c, e := o.ops.start(ctx, spec)
	if c != nil {
		o.mu.Lock()
		o.child = c
		o.mu.Unlock()
	}
	if e != nil || c == nil || !o.live(ctx) {
		return ErrUnknown
	}
	if e = p.ReleaseChild(ctx); e != nil || !o.live(ctx) {
		return ErrUnknown
	}
	in, out, stderr := p.Input(), p.Output(), p.Stderr()
	if in == nil || out == nil || stderr == nil {
		return ErrUnknown
	}
	o.mu.Lock()
	o.input, o.output, o.stderr = in, out, stderr
	o.mu.Unlock()
	return nil
}

func (o *sidRunOwner) Close(ctx context.Context) error {
	if o == nil || o.self != o {
		return ErrUnconfigured
	}
	o.closing.Store(true)
	o.mu.Lock()
	cancel := o.cancel
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	deadline, _ := ctx.Deadline()
	if end := time.Now().Add(o.config.spec.CleanupTimeout); end.Before(deadline) {
		deadline = end
	}
	cleanup, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return o.cleanupHeld(cleanup)
}

func (o *sidRunOwner) cleanupHeld(ctx context.Context) error {
	if !finite(ctx) || !o.cleanup.TryLock() {
		return ErrCleanup
	}
	defer o.cleanup.Unlock()
	o.mu.Lock()
	setup := o.setup
	unknown := o.uncertain
	o.mu.Unlock()
	if unknown {
		return ErrCleanup
	}
	if setup != nil && !sidRunJoin(ctx, setup) {
		return ErrCleanup
	}
	o.mu.Lock()
	unknown = o.uncertain
	child, pipes, wait := o.child, o.pipes, o.wait
	childConfirmed, pipesConfirmed := o.childConfirmed, o.pipesConfirmed
	o.mu.Unlock()
	if unknown {
		return ErrCleanup
	}
	if !childConfirmed && wait != nil && sidRunDone(wait) && wait.err == nil {
		childConfirmed = true
		o.mu.Lock()
		o.childConfirmed = true
		o.mu.Unlock()
	}
	if child != nil && !childConfirmed {
		if !finite(ctx) {
			return ErrCleanup
		}
		o.mu.Lock()
		w := o.stop
		if w == nil || sidRunDone(w) && w.err != nil {
			w = newSIDRunWork()
			o.stop = w
			go o.run(w, true, nil, func() ([]byte, error) { return nil, child.Close(ctx) })
		}
		o.mu.Unlock()
		if !sidRunJoin(ctx, w) || w.err != nil {
			return ErrCleanup
		}
		o.mu.Lock()
		o.childConfirmed = true
		o.mu.Unlock()
	}
	// 停止未確認の child に cleanup の EOF を渡さない。全 pipe は保持する。
	if pipes != nil && !pipesConfirmed {
		if !finite(ctx) {
			return ErrCleanup
		}
		o.mu.Lock()
		w := o.pipeClose
		if w == nil || sidRunDone(w) && w.err != nil {
			w = newSIDRunWork()
			o.pipeClose = w
			go o.run(w, true, nil, func() ([]byte, error) { return nil, pipes.Close(ctx) })
		}
		o.mu.Unlock()
		if !sidRunJoin(ctx, w) || w.err != nil {
			return ErrCleanup
		}
		o.mu.Lock()
		o.pipesConfirmed = true
		o.mu.Unlock()
	}
	o.mu.Lock()
	tasks := append([]*sidRunWork(nil), o.tasks...)
	unknown = o.uncertain
	o.mu.Unlock()
	if unknown {
		return ErrCleanup
	}
	for _, w := range tasks {
		if !sidRunJoin(ctx, w) {
			return ErrCleanup
		}
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	return nil
}
