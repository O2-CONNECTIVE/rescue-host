package bootstrapio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeOperation struct {
	mu            sync.Mutex
	n             uint32
	err           error
	done          bool
	cancelled     bool
	released      bool
	data          []byte
	polls         int
	completeAfter int
	pollGate      <-chan struct{}
	pollEntered   chan<- struct{}
	doneOnGate    bool
	onPoll        func()
}

func (o *fakeOperation) poll() (bool, uint32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.polls++
	if o.onPoll != nil {
		o.onPoll()
	}
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
func (o *fakeOperation) cancel() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cancelled = true
	return nil
}
func (o *fakeOperation) release() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.done {
		panic("未完了解放")
	}
	o.released = true
	return nil
}

type fakeKernel struct {
	ops          []*fakeOperation
	starts       int
	closes       []uintptr
	descriptor   string
	name         string
	failOpen     bool
	failClose    bool
	written      []byte
	createHandle uintptr
	customCreate bool
	onStart      func(ioKind)
}

func (k *fakeKernel) create(name, sd string) (uintptr, error) {
	k.name = name
	k.descriptor = sd
	if k.customCreate {
		return k.createHandle, nil
	}
	return 10, nil
}
func (k *fakeKernel) open(string) (uintptr, error) {
	if k.failOpen {
		return 0, errors.New("open")
	}
	return 20, nil
}
func (k *fakeKernel) duplicate(uintptr) (uintptr, error) { return 30, nil }
func (k *fakeKernel) close(h uintptr) error {
	k.closes = append(k.closes, h)
	if k.failClose {
		return errors.New("close")
	}
	return nil
}
func (k *fakeKernel) start(kind ioKind, h uintptr, b []byte) (operation, error) {
	o := k.ops[k.starts]
	k.starts++
	if k.onStart != nil {
		k.onStart(kind)
	}
	if kind == writeIO {
		k.written = append([]byte(nil), b...)
	}
	if kind == readIO {
		copy(b, o.data)
	}
	return o, nil
}
func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
func setup() Setup {
	return Setup{UserSID: "S-1-5-21-1", ServiceSID: "S-1-5-80-2", CleanupTimeout: time.Millisecond}
}
func TestPairPublishesOnceAndClosesWriter(t *testing.T) {
	k := &fakeKernel{ops: []*fakeOperation{{done: true}, {done: true, n: 3}}}
	p, err := newPair(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if p.ChildReadHandle() != 30 {
		t.Fatal("専用read複製がない")
	}
	if k.descriptor != "O:S-1-5-21-1D:P(A;;GA;;;S-1-5-80-2)" {
		t.Fatal(k.descriptor)
	}
	if len(k.name) != len(`\\.\pipe\rescue-bootstrap-`)+64 {
		t.Fatal(k.name)
	}
	if err = p.Publish(deadline(t), []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if err = p.Publish(deadline(t), []byte("{}\n")); !errors.Is(err, ErrConsumed) {
		t.Fatal(err)
	}
	if string(k.written) != "{}\n" {
		t.Fatal("送信内容")
	}
	if len(k.closes) != 1 || k.closes[0] != 20 {
		t.Fatal(k.closes)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}
func TestReaderRequiresMessageThenRealBrokenPipe(t *testing.T) {
	for _, tc := range []struct {
		name     string
		first    uint32
		firstErr error
		last     uint32
		lastErr  error
		want     bool
	}{
		{"real EOF", 3, nil, 0, errBrokenPipe, true},
		{"empty message", 0, nil, 0, errBrokenPipe, false},
		{"extra message", 3, nil, 1, nil, false},
		{"empty extra message", 3, nil, 0, nil, false},
		{"oversize message", 3, errMoreData, 0, errBrokenPipe, false},
		{"cancel is not EOF", 3, nil, 0, context.Canceled, false},
		{"broken pipe with extra data", 3, nil, 1, errBrokenPipe, false},
		{"canceled broken pipe", 3, nil, 0, errors.Join(errBrokenPipe, context.Canceled), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := &fakeKernel{ops: []*fakeOperation{{done: true, n: tc.first, err: tc.firstErr, data: []byte("{}\n")}, {done: true, n: tc.last, err: tc.lastErr}}}
			r, err := adoptReader(deadline(t), 40, time.Millisecond, k)
			if err != nil {
				t.Fatal(err)
			}
			b, err := r.ReadOnce(deadline(t))
			if (err == nil) != tc.want {
				t.Fatalf("%q %v", b, err)
			}
			if tc.want && string(b) != "{}\n" {
				t.Fatal(string(b))
			}
			if _, err = r.ReadOnce(deadline(t)); !errors.Is(err, ErrConsumed) {
				t.Fatal(err)
			}
			if err = r.Close(deadline(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestZeroNativeHandlesRejectedWithoutLosingOwner(t *testing.T) {
	k := &fakeKernel{createHandle: 0, customCreate: true}
	p, err := newPair(deadline(t), setup(), k)
	if p != nil || err == nil {
		t.Fatalf("zero OS handle accepted: %v %v", p, err)
	}
	if k.starts != 0 {
		t.Fatal("zero handleにI/Oした")
	}
}
func TestConstructorRetainsPendingConnectUntilCloseConfirmsSameOperation(t *testing.T) {
	op := &fakeOperation{}
	k := &fakeKernel{ops: []*fakeOperation{op}, failOpen: true}
	p, err := newPair(deadline(t), setup(), k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("ownerを失った: %v %v", p, err)
	}
	if !op.cancelled || op.released || len(k.closes) != 0 {
		t.Fatal("取消完了前に解放した")
	}
	if err = p.Close(deadline(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if k.starts != 1 {
		t.Fatal("別I/Oで回収を代用した")
	}
	op.done = true
	op.err = context.Canceled
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if !op.released || len(k.closes) != 1 || k.closes[0] != 10 {
		t.Fatal(k.closes)
	}
}
func TestPartialWriteBurnsAndPendingWriteRetainsOwner(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "pending"}[pending], func(t *testing.T) {
			write := &fakeOperation{done: !pending, n: 1}
			k := &fakeKernel{ops: []*fakeOperation{{done: true}, write}}
			p, err := newPair(deadline(t), setup(), k)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			err = p.Publish(ctx, []byte("{}\n"))
			if err == nil {
				t.Fatal("失敗を受理")
			}
			if err = p.Publish(deadline(t), []byte("{}\n")); !errors.Is(err, ErrConsumed) {
				t.Fatal(err)
			}
			if pending {
				if !write.cancelled || write.released || len(k.closes) != 0 {
					t.Fatal("未完了writeを解放")
				}
				write.done = true
				write.err = context.Canceled
			}
			if err = p.Close(deadline(t)); err != nil {
				t.Fatal(err)
			}
			if k.starts != 2 {
				t.Fatal("再送した")
			}
		})
	}
}
func TestReleaseChildReadReleasesBothServiceReadCopies(t *testing.T) {
	k := &fakeKernel{ops: []*fakeOperation{{done: true}}}
	p, err := newPair(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.ReleaseChildRead(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if p.ChildReadHandle() != 0 {
		t.Fatal("child readを再貸出")
	}
	if len(k.closes) != 2 || k.closes[0] != 30 || k.closes[1] != 10 {
		t.Fatalf("service側read copyが残る: %v", k.closes)
	}
	if err = p.ReleaseChildRead(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if len(k.closes) != 2 {
		t.Fatal("二重close")
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}

func (o *fakeOperation) complete(n uint32, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.done = true
	o.n = n
	o.err = err
}
func TestConcurrentCloseCancelsPublishEvenIfOperationCompletesSuccessfully(t *testing.T) {
	started := make(chan struct{})
	write := &fakeOperation{}
	k := &fakeKernel{ops: []*fakeOperation{{done: true}, write}, onStart: func(kind ioKind) {
		if kind == writeIO {
			close(started)
		}
	}}
	p, err := newPair(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- p.Publish(deadline(t), []byte("{}\n")) }()
	<-started
	if err = p.Close(deadline(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	write.complete(3, nil)
	if err = <-result; err == nil {
		t.Fatal("selfCloseで成功を返した")
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}
func TestReaderSelfCloseCannotBecomeRealEOFSucess(t *testing.T) {
	started := make(chan struct{})
	read := &fakeOperation{}
	k := &fakeKernel{ops: []*fakeOperation{read}, onStart: func(kind ioKind) { close(started) }}
	r, err := adoptReader(deadline(t), 40, time.Millisecond, k)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	ctx := deadline(t)
	go func() { _, err := r.ReadOnce(ctx); result <- err }()
	<-started
	if err = r.Close(deadline(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	read.complete(0, errBrokenPipe)
	if err = <-result; err == nil {
		t.Fatal("selfCloseをEOF成功扱い")
	}
	if err = r.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidSetupAndNoDeadlineNeverReachKernel(t *testing.T) {
	for _, tc := range []Setup{
		{}, {UserSID: "S-1-5-21-1", ServiceSID: "S-1-5-80-2"},
		{UserSID: "S-1-5-21-1D:(A;;GA;;;WD)", ServiceSID: "S-1-5-80-2", CleanupTimeout: time.Second},
		{UserSID: "S-1-05-21-1", ServiceSID: "S-1-5-80-2", CleanupTimeout: time.Second},
	} {
		p, err := newPair(deadline(t), tc, &fakeKernel{})
		if p != nil || !errors.Is(err, ErrInvalid) {
			t.Fatal(p, err)
		}
	}
	if p, err := newPair(context.Background(), setup(), &fakeKernel{}); p != nil || !errors.Is(err, ErrInvalid) {
		t.Fatal(p, err)
	}
}
func TestPairNamesAreFreshAndFrameLimitIsInclusive(t *testing.T) {
	var previous string
	for _, n := range []int{MaxRecordBytes, MaxRecordBytes + 1} {
		k := &fakeKernel{ops: []*fakeOperation{{done: true}, {done: true, n: uint32(n)}}}
		p, err := newPair(deadline(t), setup(), k)
		if err != nil {
			t.Fatal(err)
		}
		if k.name == previous {
			t.Fatal("pipe名を再利用")
		}
		previous = k.name
		b := make([]byte, n)
		b[n-1] = '\n'
		err = p.Publish(deadline(t), b)
		if (err == nil) != (n == MaxRecordBytes) {
			t.Fatal(n, err)
		}
		if err = p.Close(deadline(t)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRejectedPublishReportsAndRetainsFailedWriterCleanup(t *testing.T) {
	k := &fakeKernel{ops: []*fakeOperation{{done: true}}, failClose: true}
	p, err := newPair(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	err = p.Publish(deadline(t), nil)
	if !errors.Is(err, ErrFraming) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("cleanup不明を隠した", err)
	}
	if err = p.Publish(deadline(t), []byte("{}\n")); !errors.Is(err, ErrConsumed) {
		t.Fatal(err)
	}
	k.failClose = false
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if k.starts != 1 {
		t.Fatal("不正recordを送信")
	}
}

func TestCanceledClosePollsOnceAndRetainsPendingOperation(t *testing.T) {
	op := &fakeOperation{}
	k := &fakeKernel{ops: []*fakeOperation{op}, failOpen: true}
	s := setup()
	s.CleanupTimeout = time.Second
	p, err := newPair(deadline(t), s, k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("partial ownerを準備できない", p, err)
	}
	op.mu.Lock()
	op.polls = 0
	op.completeAfter = 3
	op.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	err = p.Close(ctx)
	op.mu.Lock()
	polls, released := op.polls, op.released
	op.mu.Unlock()
	if !errors.Is(err, ErrUnconfirmed) || polls != 1 || released || len(k.closes) != 0 {
		t.Fatalf("既cancel後に待機/解放した: error=%v polls=%d released=%v closes=%v", err, polls, released, k.closes)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	op.mu.Lock()
	polls, released = op.polls, op.released
	op.mu.Unlock()
	if polls != 3 || !released || k.starts != 1 || len(k.closes) != 1 || k.closes[0] != 10 {
		t.Fatal("同じoperationを回収できない", polls, released, k.starts, k.closes)
	}
}

func TestCleanupAdmissionHonorsCanceledCallerWhileAnotherCloseOwnsCleanup(t *testing.T) {
	op := &fakeOperation{}
	k := &fakeKernel{ops: []*fakeOperation{op}, failOpen: true}
	p, err := newPair(deadline(t), setup(), k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(p, err)
	}
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	op.mu.Lock()
	op.pollGate = gate
	op.pollEntered = entered
	op.doneOnGate = true
	op.mu.Unlock()
	longResult := make(chan error, 1)
	var release sync.Once
	longCtx := deadline(t)
	go func() { longResult <- p.Close(longCtx) }()
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	shortResult := make(chan []error, 1)
	go func() { shortResult <- []error{p.Close(ctx), p.ReleaseChildRead(ctx), p.Publish(ctx, nil)} }()
	select {
	case errs := <-shortResult:
		if !errors.Is(errs[0], ErrUnconfirmed) || !errors.Is(errs[1], ErrUnconfirmed) || !errors.Is(errs[2], ErrConsumed) {
			t.Fatal(errs)
		}
	case <-time.After(time.Second):
		release.Do(func() { close(gate) })
		<-longResult
		t.Fatal("既cancel callerが別Closeのcleanup admissionを待った")
	}
	select {
	case err := <-longResult:
		t.Fatal("gate開放前にlong Closeが終わった", err)
	default:
	}
	release.Do(func() { close(gate) })
	if err = <-longResult; err != nil {
		t.Fatal(err)
	}
	if len(k.closes) != 1 || k.closes[0] != 10 {
		t.Fatal(k.closes)
	}
}
func TestCleanupRequiresFiniteContextWithoutTouchingOwnedHandles(t *testing.T) {
	k := &fakeKernel{ops: []*fakeOperation{{done: true}}}
	p, err := newPair(deadline(t), setup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Close(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = p.ReleaseChildRead(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if len(k.closes) != 0 || p.ChildReadHandle() != 30 {
		t.Fatal("無期限contextでownerを変更", k.closes)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationDuringCloseDoesNotExtendCallerDeadline(t *testing.T) {
	op := &fakeOperation{}
	k := &fakeKernel{ops: []*fakeOperation{op}, failOpen: true}
	p, err := newPair(deadline(t), setup(), k)
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(p, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	op.mu.Lock()
	op.polls = 0
	op.completeAfter = 3
	op.onPoll = cancel
	op.mu.Unlock()
	err = p.Close(ctx)
	op.mu.Lock()
	polls, released := op.polls, op.released
	op.onPoll = nil
	op.mu.Unlock()
	if !errors.Is(err, ErrUnconfirmed) || polls != 1 || released || len(k.closes) != 0 {
		t.Fatal("cleanup途中の取消後に待機した", err, polls, released, k.closes)
	}
	if err = p.Close(deadline(t)); err != nil {
		t.Fatal(err)
	}
	if k.starts != 1 {
		t.Fatal("別operationを作成")
	}
}
