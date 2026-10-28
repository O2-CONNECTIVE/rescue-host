package childstdio

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeOp struct {
	mu                  sync.Mutex
	done                bool
	n                   uint32
	err                 error
	cancelled, released bool
	buffer              []byte
	data                []byte
	polls               int
	completeAfter       int
	pollGate            <-chan struct{}
	pollEntered         chan<- struct{}
	doneOnGate          bool
}

func (o *fakeOp) poll() (bool, uint32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.polls++
	if o.pollGate != nil {
		select {
		case o.pollEntered <- struct{}{}:
		default:
		}
		<-o.pollGate
		if o.doneOnGate {
			o.done = true
		}
	}
	if o.completeAfter > 0 && o.polls >= o.completeAfter {
		o.done = true
	}
	return o.done, o.n, o.err
}
func (o *fakeOp) cancel() error { o.mu.Lock(); defer o.mu.Unlock(); o.cancelled = true; return nil }
func (o *fakeOp) release() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.done {
		panic("未完了解放")
	}
	o.released = true
	return nil
}
func (o *fakeOp) finish(n uint32, err error, data []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	copy(o.buffer, data)
	o.n = n
	o.err = err
	o.done = true
}

type fakeKernel struct {
	mu             sync.Mutex
	next           uintptr
	names, sds     []string
	directions     []direction
	closes         []uintptr
	ops            []*fakeOp
	created        []*fakeOp
	startSignal    chan ioKind
	failOpen       bool
	connectPending bool
	onOpen         func()
}

func (k *fakeKernel) handle() uintptr { k.next++; return 100 + k.next }
func (k *fakeKernel) create(name, sd string, d direction) (uintptr, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.names = append(k.names, name)
	k.sds = append(k.sds, sd)
	k.directions = append(k.directions, d)
	return k.handle(), nil
}
func (k *fakeKernel) open(_ string, _ direction) (uintptr, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.onOpen != nil {
		k.onOpen()
	}
	if k.failOpen {
		return 0, errors.New("open")
	}
	return k.handle(), nil
}
func (k *fakeKernel) duplicate(uintptr) (uintptr, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.handle(), nil
}
func (k *fakeKernel) close(h uintptr) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closes = append(k.closes, h)
	return nil
}
func (k *fakeKernel) start(kind ioKind, h uintptr, b []byte) (operation, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var op *fakeOp
	if kind == connectIO {
		op = &fakeOp{done: !k.connectPending}
	} else {
		op = k.ops[0]
		k.ops = k.ops[1:]
		op.buffer = b
		if kind == readIO {
			copy(b, op.data)
		}
	}
	k.created = append(k.created, op)
	if k.startSignal != nil {
		k.startSignal <- kind
	}
	return op, nil
}
func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
func setup() Setup {
	return Setup{UserSID: "S-1-5-21-1", ServiceSID: "S-1-5-80-2", CleanupTimeout: 10 * time.Millisecond}
}
func TestCreatesThreeProtectedPipesAndReleasesOnlyServiceChildCopies(t *testing.T) {
	k := &fakeKernel{}
	p, err := newPipes(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	h := p.ChildHandles()
	if h.Stdin != 103 || h.Stdout != 106 || h.Stderr != 109 {
		t.Fatal(h)
	}
	if len(k.names) != 3 || k.names[0] == k.names[1] || k.names[1] == k.names[2] {
		t.Fatal(k.names)
	}
	for _, sd := range k.sds {
		if sd != "O:S-1-5-21-1D:P(A;;GA;;;S-1-5-80-2)" {
			t.Fatal(sd)
		}
	}
	if k.directions[0] != parentWrites || k.directions[1] != parentReads || k.directions[2] != parentReads {
		t.Fatal(k.directions)
	}
	if len(k.closes) != 3 || k.closes[0] != 102 || k.closes[1] != 105 || k.closes[2] != 108 {
		t.Fatal(k.closes)
	}
	if p.Input() == nil || p.Output() == nil || p.Stderr() == nil {
		t.Fatal("streamがない")
	}
	if err = p.ReleaseChild(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if p.ChildHandles().Stdin != 0 || len(k.closes) != 6 {
		t.Fatal(k.closes)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if len(k.closes) != 9 {
		t.Fatal(k.closes)
	}
}

func TestParentStreamsTransferBytesAndOnlyRealBrokenPipeIsEOF(t *testing.T) {
	write := &fakeOp{done: true, n: 3}
	read := &fakeOp{done: true, n: 3, data: []byte("abc")}
	eof := &fakeOp{done: true, err: errBrokenPipe}
	k := &fakeKernel{ops: []*fakeOp{write, read, eof}}
	p, err := newPipes(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := p.Input().Write([]byte("xyz")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if string(write.buffer) != "xyz" {
		t.Fatal(string(write.buffer))
	}
	b := make([]byte, 8)
	if n, err := p.Output().Read(b); n != 3 || err != nil || string(b[:n]) != "abc" {
		t.Fatal(n, err, string(b))
	}
	if n, err := p.Output().Read(b); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal(n, err)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}
func TestCancellationStatusCannotBecomeEOF(t *testing.T) {
	k := &fakeKernel{ops: []*fakeOp{{done: true, err: errors.Join(errBrokenPipe, io.ErrClosedPipe)}}}
	p, err := newPipes(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Output().Read(make([]byte, 4))
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("取消を実EOFとして受け入れた", err)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}

func (k *fakeKernel) closed(h uintptr) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, v := range k.closes {
		if v == h {
			return true
		}
	}
	return false
}
func TestCloseReturnsPendingReadWithoutExposingCallerBufferToKernel(t *testing.T) {
	op := &fakeOp{}
	k := &fakeKernel{ops: []*fakeOp{op}, startSignal: make(chan ioKind, 4)}
	p, err := newPipes(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		<-k.startSignal
	}
	caller := []byte("original")
	result := make(chan error, 1)
	go func() { _, err := p.Output().Read(caller); result <- err }()
	if kind := <-k.startSignal; kind != readIO {
		t.Fatal(kind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	if err = p.Close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	op.mu.Lock()
	released, cancelled := op.released, op.cancelled
	op.mu.Unlock()
	if released || !cancelled || k.closed(104) {
		t.Fatal("未完了readを解放")
	}
	copy(caller, []byte("reused!!"))
	op.finish(3, nil, []byte("bad"))
	if string(caller) != "reused!!" {
		t.Fatal("kernelが戻ったReadのbufferを書いた", string(caller))
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if !k.closed(104) {
		t.Fatal("同じparent handleを回収しない")
	}
}
func TestStreamCloseRetainsOwnedWriteCopyUntilSamePipesCleanup(t *testing.T) {
	op := &fakeOp{}
	k := &fakeKernel{ops: []*fakeOp{op}, startSignal: make(chan ioKind, 4)}
	p, err := newPipes(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		<-k.startSignal
	}
	caller := []byte("abc")
	result := make(chan error, 1)
	go func() { _, err := p.Input().Write(caller); result <- err }()
	if kind := <-k.startSignal; kind != writeIO {
		t.Fatal(kind)
	}
	if err = p.Input().Close(); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	caller[0] = 'Z'
	op.mu.Lock()
	copyHeld := string(op.buffer)
	released := op.released
	op.mu.Unlock()
	if copyHeld != "abc" || released || k.closed(101) {
		t.Fatal("owned write copyを失った", copyHeld, released)
	}
	op.finish(0, io.ErrClosedPipe, nil)
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	op.mu.Lock()
	released = op.released
	op.mu.Unlock()
	if !released || !k.closed(101) {
		t.Fatal("後続Closeで同じopを回収しない")
	}
}
func TestPartialConstructorRetainsPendingConnectAndRejectsUnboundedCleanup(t *testing.T) {
	k := &fakeKernel{connectPending: true, failOpen: true}
	p, err := newPipes(deadline(t), setup(), k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(p, err)
	}
	if p.ChildHandles().Stdin != 0 || k.closed(101) {
		t.Fatal("未完了constructorを解放")
	}
	if err = p.Close(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	op := k.created[0]
	op.finish(0, nil, nil)
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if !k.closed(101) || len(k.created) != 1 {
		t.Fatal("別opで回収を代用")
	}
}
func TestIdleReadHasNoImplicitTimeoutAndPendingSuccessZeroIsNotEOF(t *testing.T) {
	op := &fakeOp{}
	k := &fakeKernel{ops: []*fakeOp{op}, startSignal: make(chan ioKind, 4)}
	p, err := newPipes(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		<-k.startSignal
	}
	result := make(chan error, 1)
	go func() { _, err := p.Stderr().Read(make([]byte, 4)); result <- err }()
	<-k.startSignal
	// Setupのcleanup予算が経過しても通常idle readは終わらない。
	timer := time.NewTimer(3 * setup().CleanupTimeout)
	select {
	case err := <-result:
		timer.Stop()
		t.Fatal("idleにcleanup timeoutを適用", err)
	case <-timer.C:
	}
	op.finish(0, nil, nil)
	if err = <-result; err != nil {
		t.Fatal("0 byte成功をEOF扱い", err)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}
func TestUnconstructedPipesRefusesStreamsAndCleanup(t *testing.T) {
	p := &Pipes{}
	if p.Input() != nil || p.Output() != nil || p.Stderr() != nil {
		t.Fatal("未構成Pipesがstreamを返した")
	}
	if err := p.ReleaseChild(deadline(t)); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	if err := p.Close(deadline(t)); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
}

func TestCanceledCleanupPollsOnceThenFreshCloseReclaimsSameConnect(t *testing.T) {
	k := &fakeKernel{connectPending: true, failOpen: true}
	p, err := newPipes(deadline(t), setup(), k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(p, err)
	}
	op := k.created[0]
	op.mu.Lock()
	op.polls = 0
	op.completeAfter = 3
	op.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	if err = p.Close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	op.mu.Lock()
	polls, released := op.polls, op.released
	op.mu.Unlock()
	if polls != 1 || released || k.closed(101) {
		t.Fatal("取消後の待機で所有を失った", polls, released)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	op.mu.Lock()
	polls = op.polls
	op.mu.Unlock()
	if polls != 3 || len(k.created) != 1 || !k.closed(101) {
		t.Fatal("同じconnectを回収しない", polls)
	}
}
func TestCanceledCleanupAdmissionDoesNotWaitForAnotherCollector(t *testing.T) {
	k := &fakeKernel{connectPending: true, failOpen: true}
	p, err := newPipes(deadline(t), setup(), k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(p, err)
	}
	op := k.created[0]
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	op.mu.Lock()
	op.pollGate = gate
	op.pollEntered = entered
	op.doneOnGate = true
	op.mu.Unlock()
	longResult := make(chan error, 1)
	longCtx := deadline(t)
	go func() { longResult <- p.Close(longCtx) }()
	<-entered
	var release sync.Once
	joined := false
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		if !joined {
			<-longResult
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	shortResult := make(chan []error, 1)
	go func() { shortResult <- []error{p.Close(ctx), p.ReleaseChild(ctx)} }()
	select {
	case errs := <-shortResult:
		if !errors.Is(errs[0], ErrUnconfirmed) || !errors.Is(errs[1], ErrUnconfirmed) {
			t.Fatal(errs)
		}
	case <-time.After(time.Second):
		release.Do(func() { close(gate) })
		<-shortResult
		t.Fatal("別collectorのadmissionを待った")
	}
	select {
	case err := <-longResult:
		joined = true
		t.Fatal("gate開放前にcollectorが終了", err)
	default:
	}
	release.Do(func() { close(gate) })
	err = <-longResult
	joined = true
	if err != nil {
		t.Fatal(err)
	}
}
func TestInvalidPublicSetupNeverReachesNativeFactory(t *testing.T) {
	for _, s := range []Setup{{}, {UserSID: "S-1-5-21-1D:(A;;GA;;;WD)", ServiceSID: "S-1-5-80-2", CleanupTimeout: time.Second}, {UserSID: "S-1-5-21-1", ServiceSID: "S-1-5-80-2", CleanupTimeout: -time.Second}} {
		if p, err := NewNative(deadline(t), s); p != nil || !errors.Is(err, ErrConfig) {
			t.Fatal(p, err)
		}
	}
	if p, err := NewNative(context.Background(), setup()); p != nil || !errors.Is(err, ErrConfig) {
		t.Fatal(p, err)
	}
}

func TestStreamCloseDoesOnePollWithLongSetupBudgetAndRetainsPendingOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	k := &fakeKernel{connectPending: true, failOpen: true, onOpen: cancel}
	s := setup()
	s.CleanupTimeout = time.Second
	p, err := newPipes(ctx, s, k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(p, err)
	}
	op := k.created[0]
	op.mu.Lock()
	op.polls = 0
	op.completeAfter = 3
	op.mu.Unlock()
	err = p.Input().Close()
	op.mu.Lock()
	polls, released, cancelled := op.polls, op.released, op.cancelled
	op.mu.Unlock()
	if !errors.Is(err, ErrUnconfirmed) || polls != 1 || released || !cancelled || k.closed(101) {
		t.Fatal("stream.CloseがSetup予算で待機/解放した", err, polls, released, cancelled)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	op.mu.Lock()
	polls, released = op.polls, op.released
	op.mu.Unlock()
	if polls != 3 || !released || !k.closed(101) || len(k.created) != 1 {
		t.Fatal("同じPipesで回収できない", polls, released)
	}
}
func TestStreamAndPipesCloseReturnBeforeAnotherPollReleasesItsMutex(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	op := &fakeOp{pollGate: gate, pollEntered: entered, doneOnGate: true}
	k := &fakeKernel{ops: []*fakeOp{op}}
	s := setup()
	s.CleanupTimeout = time.Second
	p, err := newPipes(deadline(t), s, k)
	if err != nil {
		t.Fatal(err)
	}
	readResult := make(chan error, 1)
	go func() { _, err := p.Output().Read(make([]byte, 8)); readResult <- err }()
	<-entered
	var release sync.Once
	readJoined := false
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		if !readJoined {
			<-readResult
		}
	})
	streamResult := make(chan error, 1)
	go func() { streamResult <- p.Output().Close() }()
	select {
	case err := <-streamResult:
		if !errors.Is(err, ErrUnconfirmed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream.Closeが別pollのGo mutexを待った")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	pipesResult := make(chan error, 1)
	go func() { pipesResult <- p.Close(ctx) }()
	select {
	case err := <-pipesResult:
		if !errors.Is(err, ErrUnconfirmed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pipes.Closeがcaller期限を越えて別pollのGo mutexを待った")
	}
	if k.closed(104) {
		t.Fatal("実completion前にparent handleを閉じた")
	}
	select {
	case err := <-readResult:
		readJoined = true
		t.Fatal("gate開放前にReadが終わった", err)
	default:
	}
	release.Do(func() { close(gate) })
	err = <-readResult
	readJoined = true
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if !k.closed(104) {
		t.Fatal("同じPipesで後続回収できない")
	}
}
