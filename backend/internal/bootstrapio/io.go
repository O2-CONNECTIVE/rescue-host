// Package bootstrapio は認証済み呼出元の一回限りbootstrap搬送を所有する。
// 認証、grant生成、recordのJSON解釈はこのpackageの責務ではない。
package bootstrapio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxRecordBytes = 16384

var (
	ErrUnavailable = errors.New("bootstrap native I/O unavailable")
	ErrInvalid     = errors.New("invalid bootstrap I/O setup")
	ErrConsumed    = errors.New("bootstrap send/read right consumed")
	ErrUnconfirmed = errors.New("bootstrap I/O cleanup unconfirmed; retain owner and retry Close")
	ErrFraming     = errors.New("bootstrap message or EOF invalid")
	errMoreData    = errors.New("message exceeds buffer")
	errBrokenPipe  = errors.New("real broken pipe")
)

type Setup struct {
	UserSID, ServiceSID string
	CleanupTimeout      time.Duration
}
type ioKind uint8

const (
	connectIO ioKind = iota
	readIO
	writeIO
)

// private seam。公開factoryは常にnative kernelだけを選ぶ。
type kernel interface {
	create(string, string) (uintptr, error)
	open(string) (uintptr, error)
	duplicate(uintptr) (uintptr, error)
	close(uintptr) error
	start(ioKind, uintptr, []byte) (operation, error)
}
type operation interface {
	poll() (bool, uint32, error)
	cancel() error
	release() error
}
type ownedIO struct {
	mu                        sync.Mutex
	op                        operation
	done, released, cancelled bool
	n                         uint32
	err                       error
}

func (o *ownedIO) step(cancel bool) (bool, uint32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cancel && !o.done && !o.cancelled {
		o.cancelled = true
		_ = o.op.cancel()
	}
	if !o.done {
		o.done, o.n, o.err = o.op.poll()
	}
	if !o.done {
		return false, 0, nil
	}
	if !o.released {
		if err := o.op.release(); err != nil {
			return false, 0, err
		}
		o.released = true
	}
	if o.cancelled {
		return true, o.n, errors.Join(context.Canceled, o.err)
	}
	return true, o.n, o.err
}
func (o *ownedIO) wait(ctx context.Context, budget time.Duration, cancel bool) (uint32, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, ErrInvalid
	}
	end := deadline
	if cancel {
		budgetEnd := time.Now().Add(budget)
		if budgetEnd.Before(end) {
			end = budgetEnd
		}
	}
	for {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			cancel = true
		}
		done, n, err := o.step(cancel)
		if done {
			if cancel {
				return 0, errors.Join(ctx.Err(), context.Canceled, err)
			}
			return n, err
		}
		// 取消済みcontextではCancelIoExと一度の非blocking pollだけで返す。
		// 元のdeadlineを内部cleanup予算で延ばさず、ownerを次のCloseへ残す。
		if ctx.Err() != nil || !time.Now().Before(end) {
			return 0, errors.Join(ErrUnconfirmed, ctx.Err(), err)
		}
		delay := time.Millisecond
		if remaining := time.Until(end); remaining < delay {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// cleanupにも有限deadlineを要求する。既取消contextは一度のpollには使える。
func boundedCleanup(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, ErrInvalid
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, nil, ErrInvalid
	}
	if end := time.Now().Add(budget); end.Before(deadline) {
		deadline = end
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	return bounded, cancel, nil
}
func (o *owner) admitCleanup(ctx context.Context) error {
	for {
		if o.cleanup.TryLock() {
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(ErrUnconfirmed, ctx.Err())
		}
		deadline, _ := ctx.Deadline()
		delay := time.Millisecond
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			return errors.Join(ErrUnconfirmed, context.DeadlineExceeded)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ErrUnconfirmed, ctx.Err())
		case <-timer.C:
		}
	}
}
func validContext(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
func validSID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 4 || len(parts) > 18 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for i, p := range parts[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, e := strconv.ParseUint(p, 10, bits)
		if e != nil || strconv.FormatUint(n, 10) != p {
			return false
		}
	}
	return true
}
func validSetup(s Setup) bool {
	return validSID(s.UserSID) && validSID(s.ServiceSID) && s.CleanupTimeout > 0
}

type owner struct {
	mu                 sync.Mutex
	cleanup            sync.Mutex
	api                kernel
	budget             time.Duration
	read, write, child uintptr
	closed             bool
	busy               int
	pending            []*ownedIO
}

func (o *owner) run(ctx context.Context, kind ioKind, h uintptr, b []byte) (uint32, error) {
	// 呼出側がbusyを予約している間はCloseがhandleを解放できない。
	defer func() { o.mu.Lock(); o.busy--; o.mu.Unlock() }()
	raw, err := o.api.start(kind, h, b)
	if raw == nil {
		return 0, err
	}
	op := &ownedIO{op: raw}
	o.mu.Lock()
	o.pending = append(o.pending, op)
	o.mu.Unlock()
	if err != nil {
		_, joinErr := op.wait(ctx, o.budget, true)
		return 0, errors.Join(err, joinErr)
	}
	return op.wait(ctx, o.budget, false)
}
func (o *owner) unresolved() bool {
	for _, op := range o.pending {
		op.mu.Lock()
		done := op.released
		op.mu.Unlock()
		if !done {
			return true
		}
	}
	return false
}
func (o *owner) closeHandle(h *uintptr) error {
	o.mu.Lock()
	value := *h
	o.mu.Unlock()
	if value == 0 {
		return nil
	}
	// cleanup mutexでclose同士だけを直列化し、状態mutex外でOSへ渡す。
	if err := o.api.close(value); err != nil {
		return errors.Join(ErrUnconfirmed, err)
	}
	o.mu.Lock()
	*h = 0
	o.mu.Unlock()
	return nil
}
func (o *owner) close(ctx context.Context) error {
	cleanupCtx, cancel, err := boundedCleanup(ctx, o.budget)
	if err != nil {
		return err
	}
	defer cancel()
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	if err := o.admitCleanup(cleanupCtx); err != nil {
		return err
	}
	defer o.cleanup.Unlock()
	o.mu.Lock()
	pending := append([]*ownedIO(nil), o.pending...)
	busy := o.busy
	o.mu.Unlock()
	var result error
	for _, op := range pending {
		_, err := op.wait(cleanupCtx, o.budget, true)
		if errors.Is(err, ErrUnconfirmed) {
			result = errors.Join(result, err)
		}
	}
	o.mu.Lock()
	unsafe := busy != 0 || o.busy != 0 || o.unresolved()
	o.mu.Unlock()
	if unsafe {
		return errors.Join(result, ErrUnconfirmed)
	}
	for _, h := range []*uintptr{&o.write, &o.child, &o.read} {
		result = errors.Join(result, o.closeHandle(h))
	}
	return result
}

type Pair struct {
	o        owner
	consumed bool
}

func NewNativePair(ctx context.Context, s Setup) (*Pair, error) {
	if !validContext(ctx) || !validSetup(s) {
		return nil, ErrInvalid
	}
	k, err := nativeKernel()
	if err != nil {
		return nil, err
	}
	return newPair(ctx, s, k)
}
func newPair(ctx context.Context, s Setup, k kernel) (p *Pair, err error) {
	if !validContext(ctx) || !validSetup(s) || k == nil {
		return nil, ErrInvalid
	}
	p = &Pair{o: owner{api: k, budget: s.CleanupTimeout}}
	defer func() {
		if err != nil {
			cleanup := p.Close(ctx)
			if cleanup != nil {
				err = errors.Join(err, cleanup)
			} else {
				p = nil
			}
		}
	}()
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return p, err
	}
	name := `\\.\pipe\rescue-bootstrap-` + hex.EncodeToString(nonce[:])
	sd := "O:" + s.UserSID + "D:P(A;;GA;;;" + s.ServiceSID + ")"
	p.o.read, err = k.create(name, sd)
	if err != nil {
		return p, err
	}
	if p.o.read == 0 || p.o.read == ^uintptr(0) {
		p.o.read = 0
		return p, ErrInvalid
	}
	// connectを先に開始し、service自身が非継承writerを開いて完了させる。
	raw, startErr := k.start(connectIO, p.o.read, nil)
	if raw == nil {
		return p, errors.Join(ErrInvalid, startErr)
	}
	op := &ownedIO{op: raw}
	p.o.pending = append(p.o.pending, op)
	if startErr != nil {
		return p, startErr
	}
	p.o.write, err = k.open(name)
	if err != nil {
		return p, err
	}
	if p.o.write == 0 || p.o.write == ^uintptr(0) || p.o.write == p.o.read {
		p.o.write = 0
		return p, ErrInvalid
	}
	if _, err = op.wait(ctx, s.CleanupTimeout, false); err != nil {
		return p, err
	}
	p.o.child, err = k.duplicate(p.o.read)
	if err != nil {
		return p, err
	}
	if p.o.child == 0 || p.o.child == ^uintptr(0) || p.o.child == p.o.read || p.o.child == p.o.write {
		p.o.child = 0
		return p, ErrInvalid
	}
	return p, nil
}

// ChildReadHandle はserviceが所有する借用handle。child作成後にReleaseChildReadする。
func (p *Pair) ChildReadHandle() uintptr {
	if p == nil {
		return 0
	}
	p.o.mu.Lock()
	defer p.o.mu.Unlock()
	if p.o.closed {
		return 0
	}
	return p.o.child
}
func (p *Pair) Publish(ctx context.Context, b []byte) error {
	if p == nil {
		return ErrInvalid
	}
	p.o.mu.Lock()
	if p.consumed || p.o.closed {
		p.o.mu.Unlock()
		return ErrConsumed
	}
	p.consumed = true
	if !validContext(ctx) || len(b) == 0 || len(b) > MaxRecordBytes || b[len(b)-1] != '\n' {
		p.o.mu.Unlock()
		return errors.Join(ErrFraming, p.closeWriter(ctx))
	}
	p.o.busy++
	h := p.o.write
	p.o.mu.Unlock()
	// callerのslice変更から保護し、完了までkernel operationがcopyを保持する。
	record := append([]byte(nil), b...)
	n, err := p.o.run(ctx, writeIO, h, record)
	closeErr := p.closeWriter(ctx)
	p.o.mu.Lock()
	closed := p.o.closed
	p.o.mu.Unlock()
	if closed {
		err = errors.Join(err, context.Canceled)
	}
	if err != nil || n != uint32(len(record)) {
		return errors.Join(ErrFraming, err, closeErr)
	}
	return closeErr
}
func (p *Pair) closeWriter(ctx context.Context) error {
	cleanupCtx, cancel, err := boundedCleanup(ctx, p.o.budget)
	if err != nil {
		return errors.Join(ErrUnconfirmed, err)
	}
	defer cancel()
	if err := p.o.admitCleanup(cleanupCtx); err != nil {
		return err
	}
	defer p.o.cleanup.Unlock()
	p.o.mu.Lock()
	unsafe := p.o.busy != 0 || p.o.unresolved()
	p.o.mu.Unlock()
	if unsafe {
		return ErrUnconfirmed
	}
	return p.o.closeHandle(&p.o.write)
}
func (p *Pair) ReleaseChildRead(ctx context.Context) error {
	if p == nil {
		return ErrInvalid
	}
	cleanupCtx, cancel, err := boundedCleanup(ctx, p.o.budget)
	if err != nil {
		return err
	}
	defer cancel()
	if err := p.o.admitCleanup(cleanupCtx); err != nil {
		return err
	}
	defer p.o.cleanup.Unlock()
	p.o.mu.Lock()
	unsafe := p.o.unresolved()
	p.o.mu.Unlock()
	if unsafe {
		return ErrUnconfirmed
	}
	return errors.Join(p.o.closeHandle(&p.o.child), p.o.closeHandle(&p.o.read))
}
func (p *Pair) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.o.close(ctx)
}

// Reader は認証済みの継承server-read handleの所有権を引き取る。
// 呼出前にbootstrapauthが認証とprovenance検証を完了していなければならない。
type Reader struct {
	o        owner
	consumed bool
}

func AdoptNativeReader(ctx context.Context, h uintptr, cleanup time.Duration) (*Reader, error) {
	if !validContext(ctx) || h == 0 || h == ^uintptr(0) || cleanup <= 0 {
		return nil, ErrInvalid
	}
	k, err := nativeKernel()
	if err != nil {
		return nil, err
	}
	return adoptReader(ctx, h, cleanup, k)
}
func adoptReader(ctx context.Context, h uintptr, cleanup time.Duration, k kernel) (*Reader, error) {
	if !validContext(ctx) || h == 0 || h == ^uintptr(0) || cleanup <= 0 || k == nil {
		return nil, ErrInvalid
	}
	return &Reader{o: owner{api: k, budget: cleanup, read: h}}, nil
}
func (r *Reader) ReadOnce(ctx context.Context) ([]byte, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	r.o.mu.Lock()
	if r.consumed || r.o.closed {
		r.o.mu.Unlock()
		return nil, ErrConsumed
	}
	r.consumed = true
	if !validContext(ctx) {
		r.o.mu.Unlock()
		return nil, ErrInvalid
	}
	r.o.busy++
	h := r.o.read
	r.o.mu.Unlock()
	b := make([]byte, MaxRecordBytes)
	n, err := r.o.run(ctx, readIO, h, b)
	if err != nil || n == 0 || n > uint32(len(b)) || b[n-1] != '\n' {
		return nil, errors.Join(ErrFraming, err)
	}
	r.o.mu.Lock()
	if r.o.closed {
		r.o.mu.Unlock()
		return nil, ErrFraming
	}
	r.o.busy++
	r.o.mu.Unlock()
	// 成功した0byte messageもEOFではない。次のreadの実BROKEN_PIPEだけを受け付ける。
	extra := make([]byte, 1)
	extraN, eofErr := r.o.run(ctx, readIO, h, extra)
	err = eofErr
	if extraN != 0 || !errors.Is(err, errBrokenPipe) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return nil, errors.Join(ErrFraming, err)
	}
	r.o.mu.Lock()
	closed := r.o.closed
	r.o.mu.Unlock()
	if closed {
		return nil, ErrFraming
	}
	return append([]byte(nil), b[:n]...), nil
}
func (r *Reader) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.o.close(ctx)
}
