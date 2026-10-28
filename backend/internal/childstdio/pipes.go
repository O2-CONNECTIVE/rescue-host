// Package childstdio はWindows子の標準pipeと親の非同期I/Oの所有を扱う。
// 保護設定値やpipe名はcaller認証の証明ではない。
package childstdio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

var (
	ErrConfig      = errors.New("標準pipeの保護設定または有限contextが未設定です")
	ErrUnavailable = errors.New("Windows標準pipeを利用できません")
	ErrUnconfirmed = errors.New("標準pipeのI/O完了または回収を確認できません")
	errBrokenPipe  = errors.New("実BROKEN_PIPE")
)

type Setup struct {
	UserSID, ServiceSID string
	CleanupTimeout      time.Duration
}
type direction uint8

const (
	parentWrites direction = iota
	parentReads
)

type ioKind uint8

const (
	connectIO ioKind = iota
	readIO
	writeIO
)

// private OS境界。公開factoryはnative以外を注入できない。
type kernel interface {
	create(string, string, direction) (uintptr, error)
	open(string, direction) (uintptr, error)
	duplicate(uintptr) (uintptr, error)
	close(uintptr) error
	start(ioKind, uintptr, []byte) (operation, error)
}
type operation interface {
	poll() (bool, uint32, error)
	cancel() error
	release() error
}
type Pipes struct {
	ends          [3]*endpoint
	mu            sync.Mutex
	ready, closed bool
	budget        time.Duration
}
type endpoint struct {
	api                     kernel
	budget                  time.Duration
	direction               direction
	mu                      sync.Mutex
	cleanup                 sync.Mutex
	serial                  chan struct{}
	stop                    chan struct{}
	closed, busy            bool
	parent, original, child uintptr
	op                      *ownedOperation
}

func validSID(s string) bool {
	p := strings.Split(s, "-")
	if len(p) < 4 || len(p) > 18 || p[0] != "S" || p[1] != "1" {
		return false
	}
	for i, v := range p[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, err := strconv.ParseUint(v, 10, bits)
		if err != nil || strconv.FormatUint(n, 10) != v {
			return false
		}
	}
	return true
}
func ordinary(h uintptr) bool { return h != 0 && h < ^uintptr(15) }
func finite(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
func valid(ctx context.Context, s Setup) bool {
	return finite(ctx) && ctx.Err() == nil && validSID(s.UserSID) && validSID(s.ServiceSID) && s.CleanupTimeout > 0
}
func NewNative(ctx context.Context, s Setup) (*Pipes, error) {
	if !valid(ctx, s) {
		return nil, ErrConfig
	}
	api, err := nativeKernel()
	if err != nil {
		return nil, err
	}
	return newPipes(ctx, s, api)
}
func newPipes(ctx context.Context, s Setup, api kernel) (p *Pipes, err error) {
	if !valid(ctx, s) || api == nil {
		return nil, ErrConfig
	}
	p = &Pipes{budget: s.CleanupTimeout}
	for i := range p.ends {
		d := parentReads
		if i == 0 {
			d = parentWrites
		}
		e := &endpoint{api: api, budget: s.CleanupTimeout, direction: d, serial: make(chan struct{}, 1), stop: make(chan struct{})}
		e.serial <- struct{}{}
		p.ends[i] = e
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close(ctx))
		}
	}()
	descriptor := "O:" + s.UserSID + "D:P(A;;GA;;;" + s.ServiceSID + ")"
	for _, e := range p.ends {
		if ctx.Err() != nil {
			return p, ctx.Err()
		}
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			return p, err
		}
		name := `\\.\pipe\rescue-stdio-` + hex.EncodeToString(random[:])
		e.parent, err = api.create(name, descriptor, e.direction)
		if err != nil {
			return p, err
		}
		if !ordinary(e.parent) {
			e.parent = 0
			return p, ErrConfig
		}
		raw, startErr := api.start(connectIO, e.parent, nil)
		if raw != nil {
			e.op = &ownedOperation{raw: raw}
		}
		if startErr != nil {
			return p, startErr
		}
		if raw == nil {
			return p, ErrConfig
		}
		e.original, err = api.open(name, e.direction)
		if err != nil {
			return p, err
		}
		if !ordinary(e.original) || e.original == e.parent {
			e.original = 0
			return p, ErrConfig
		}
		if _, err = e.op.wait(ctx); err != nil {
			return p, err
		}
		e.child, err = api.duplicate(e.original)
		if err != nil {
			return p, err
		}
		if !ordinary(e.child) || e.child == e.parent || e.child == e.original {
			e.child = 0
			return p, ErrConfig
		}
		if err = e.closeHandle(&e.original); err != nil {
			return p, err
		}
	}
	p.ready = true
	return p, nil
}
func (p *Pipes) ChildHandles() childjob.Handles {
	if p == nil {
		return childjob.Handles{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready || p.closed {
		return childjob.Handles{}
	}
	var h [3]uintptr
	for i, e := range p.ends {
		e.mu.Lock()
		h[i] = e.child
		e.mu.Unlock()
	}
	return childjob.Handles{Stdin: h[0], Stdout: h[1], Stderr: h[2]}
}
func (p *Pipes) configured() bool {
	return p != nil && p.ends[0] != nil && p.ends[1] != nil && p.ends[2] != nil
}
func (p *Pipes) Input() io.WriteCloser {
	if !p.configured() {
		return nil
	}
	return &input{p.ends[0]}
}
func (p *Pipes) Output() io.ReadCloser {
	if !p.configured() {
		return nil
	}
	return &output{p.ends[1]}
}
func (p *Pipes) Stderr() io.ReadCloser {
	if !p.configured() {
		return nil
	}
	return &output{p.ends[2]}
}
func (p *Pipes) ReleaseChild(ctx context.Context) error {
	if !p.configured() || !finite(ctx) {
		return ErrConfig
	}
	cleanup, cancel := bounded(ctx, p.budget)
	defer cancel()
	var result error
	for _, e := range p.ends {
		if err := admit(cleanup, &e.cleanup); err != nil {
			result = errors.Join(result, err)
			continue
		}
		result = errors.Join(result, e.closeHandle(&e.child), e.closeHandle(&e.original))
		e.cleanup.Unlock()
	}
	return result
}
func (p *Pipes) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if !p.configured() || !finite(ctx) {
		return ErrConfig
	}
	cleanup, cancel := bounded(ctx, p.budget)
	defer cancel()
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	for _, e := range p.ends {
		e.signal()
	}
	var result error
	for _, e := range p.ends {
		result = errors.Join(result, e.collect(cleanup))
	}
	return result
}
func (e *endpoint) signal() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		close(e.stop)
	}
}
func (e *endpoint) closeHandle(slot *uintptr) error {
	e.mu.Lock()
	h := *slot
	e.mu.Unlock()
	if h == 0 {
		return nil
	}
	if err := e.api.close(h); err != nil {
		return errors.Join(ErrUnconfirmed, err)
	}
	e.mu.Lock()
	*slot = 0
	e.mu.Unlock()
	return nil
}
func (e *endpoint) collect(ctx context.Context) error {
	if err := admit(ctx, &e.cleanup); err != nil {
		return err
	}
	defer e.cleanup.Unlock()
	for {
		e.mu.Lock()
		op, busy := e.op, e.busy
		e.mu.Unlock()
		complete := op == nil
		if op != nil {
			complete, _, _ = op.tryStep(true)
		}
		if complete && !busy {
			return errors.Join(e.closeHandle(&e.original), e.closeHandle(&e.child), e.closeHandle(&e.parent))
		}
		if ctx.Err() != nil {
			return errors.Join(ErrUnconfirmed, ctx.Err())
		}
		deadline, _ := ctx.Deadline()
		if !time.Now().Before(deadline) {
			return ErrUnconfirmed
		}
		pause(ctx)
	}
}
func bounded(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	deadline, _ := ctx.Deadline()
	if end := time.Now().Add(budget); end.Before(deadline) {
		deadline = end
	}
	return context.WithDeadline(ctx, deadline)
}
func admit(ctx context.Context, mu *sync.Mutex) error {
	for {
		if mu.TryLock() {
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(ErrUnconfirmed, ctx.Err())
		}
		deadline, _ := ctx.Deadline()
		if !time.Now().Before(deadline) {
			return ErrUnconfirmed
		}
		pause(ctx)
	}
}
func pause(ctx context.Context) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
