package operatorauth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type profilePipeProducerFixture struct{ material profilePipeMaterial }

func (f profilePipeProducerFixture) prepare(context.Context) (profilePipeMaterial, error) {
	return f.material, nil
}

func profilePipeTestProducer() profilePipeProducer {
	return profilePipeProducerFixture{profilePipeMaterial{
		launchID: strings.Repeat("a", 64), cleanup: time.Second,
		profile: helperProfile{operator: tokenProjection{user: "S-1-5-21-1", authentication: 7, session: 3, integrity: 8192, kind: 2, level: 1}, guest: tokenProjection{user: "S-1-5-21-2"}},
	}}
}

type profilePipeKernelFixture struct {
	mu        sync.Mutex
	closed    []uintptr
	createFn  func() (uintptr, uintptr, error)
	closeFn   func(uintptr) error
	inheritFn func(uintptr) (uintptr, error)
}

func (f *profilePipeKernelFixture) create() (uintptr, uintptr, error) {
	if f.createFn != nil {
		return f.createFn()
	}
	return 51, 52, nil
}
func (f *profilePipeKernelFixture) inheritReader(h uintptr) (uintptr, error) {
	if f.inheritFn != nil {
		return f.inheritFn(h)
	}
	if h != 51 {
		return 0, ErrDenied
	}
	return 53, nil
}
func (f *profilePipeKernelFixture) close(h uintptr) error {
	f.mu.Lock()
	f.closed = append(f.closed, h)
	f.mu.Unlock()
	if f.closeFn != nil {
		return f.closeFn(h)
	}
	return nil
}

func TestProfilePipeReleasesParentReaderBeforeSendingAndClosesWriterForEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k := &profilePipeKernelFixture{}
	writes := make(chan []byte, 1)
	api := &profileSyncFixture{transferFn: func(kind profileSyncKind, handle uintptr, b []byte) (uint32, error) {
		if kind != profileSyncWrite || handle != 52 {
			return 0, ErrDenied
		}
		writes <- append([]byte(nil), b...)
		return uint32(len(b)), nil
	}}
	p, err := newProfilePipe(ctx, profilePipeTestProducer(), k, api)
	if err != nil || p == nil {
		t.Fatalf("construct = %v, %v", p, err)
	}
	if p.childReader() != 53 {
		t.Fatalf("child reader = %d", p.childReader())
	}
	if p.launchID() != strings.Repeat("a", 64) {
		t.Fatalf("launch correlation = %q", p.launchID())
	}
	if err = p.sendAfterResume(ctx); !errors.Is(err, ErrDenied) {
		t.Fatalf("send before reader release = %v", err)
	}
	select {
	case <-writes:
		t.Fatal("writer started while child suspended")
	default:
	}
	if err = p.releaseReader(ctx); err != nil {
		t.Fatal(err)
	}
	if err = p.sendAfterResume(ctx); err != nil {
		t.Fatal(err)
	}
	frame := <-writes
	if _, err = decodeHelperProfileFrame(frame, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err = p.sendAfterResume(ctx); !errors.Is(err, ErrDenied) {
		t.Fatalf("duplicate send = %v", err)
	}
	if err = p.close(ctx); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.closed) != 3 || k.closed[0] != 51 || k.closed[1] != 53 || k.closed[2] != 52 {
		t.Fatalf("close order = %v", k.closed)
	}
}

func TestProfilePipeUnconfiguredProducerDoesNotAllocate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k := &profilePipeKernelFixture{createFn: func() (uintptr, uintptr, error) { t.Fatal("unconfigured producer allocated pipe"); return 0, 0, nil }}
	p, err := newProfilePipe(ctx, nil, k, &profileSyncFixture{})
	if p != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("unconfigured producer = %v, %v", p, err)
	}
}

func TestProfilePipeNativeUnconfiguredProducerRejectsBeforeAllocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := newNativeProfilePipe(ctx, nil)
	if p != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("native unconfigured producer = %v, %v", p, err)
	}
}

func TestProfilePipeCancelRetainsWriterUntilActualReturnAndJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started, finish := make(chan struct{}), make(chan struct{})
	k := &profilePipeKernelFixture{}
	api := &profileSyncFixture{transferFn: func(profileSyncKind, uintptr, []byte) (uint32, error) {
		close(started)
		<-finish
		return 0, context.Canceled
	}}
	p, err := newProfilePipe(ctx, profilePipeTestProducer(), k, api)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.releaseReader(ctx); err != nil {
		t.Fatal(err)
	}
	sendCtx, stopSend := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- p.sendAfterResume(sendCtx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("writer never started")
	}
	stopSend()
	err = <-result
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("blocked writer = %v", err)
	}
	short, done := context.WithTimeout(context.Background(), 15*time.Millisecond)
	err = p.close(short)
	done()
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("blocked cleanup = %v", err)
	}
	k.mu.Lock()
	closed := append([]uintptr(nil), k.closed...)
	k.mu.Unlock()
	for _, h := range closed {
		if h == 52 {
			t.Fatal("writer closed before actual syscall return")
		}
	}
	close(finish)
	if err = p.close(ctx); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.closed != 1 {
		t.Fatalf("unjoined writer thread: %d", api.closed)
	}
}

func TestProfilePipePartialAcquisitionRetainsEndpointsForCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k := &profilePipeKernelFixture{createFn: func() (uintptr, uintptr, error) { return 51, 52, errors.New("partial create") }}
	p, err := newProfilePipe(ctx, profilePipeTestProducer(), k, &profileSyncFixture{})
	if p == nil || err == nil {
		t.Fatalf("partial acquisition = %v, %v", p, err)
	}
	if err = p.close(ctx); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.closed) != 2 || k.closed[0] != 51 || k.closed[1] != 52 {
		t.Fatalf("partial endpoints = %v", k.closed)
	}
}

func TestProfilePipePartialInheritedReaderRetainedOnDuplicateError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k := &profilePipeKernelFixture{inheritFn: func(uintptr) (uintptr, error) { return 53, errors.New("partial reader duplicate") }}
	p, err := newProfilePipe(ctx, profilePipeTestProducer(), k, &profileSyncFixture{})
	if p == nil || err == nil {
		t.Fatalf("partial reader = %v, %v", p, err)
	}
	if p.childReader() != 0 {
		t.Fatal("failed constructor supplied child reader")
	}
	if err = p.close(ctx); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.closed) != 3 || k.closed[0] != 51 || k.closed[1] != 53 || k.closed[2] != 52 {
		t.Fatalf("partial reader cleanup = %v", k.closed)
	}
}

func TestProfilePipePendingCloseIsRetainedAndNotDuplicated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered, finish := make(chan struct{}), make(chan struct{})
	k := &profilePipeKernelFixture{closeFn: func(h uintptr) error {
		if h == 53 {
			close(entered)
			<-finish
		}
		return nil
	}}
	p, err := newProfilePipe(ctx, profilePipeTestProducer(), k, &profileSyncFixture{})
	if err != nil {
		t.Fatal(err)
	}
	short, done := context.WithTimeout(context.Background(), 15*time.Millisecond)
	err = p.releaseReader(short)
	done()
	<-entered
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("pending reader close = %v", err)
	}
	if err = p.sendAfterResume(ctx); !errors.Is(err, ErrDenied) {
		t.Fatalf("send after unconfirmed reader close = %v", err)
	}
	close(finish)
	if err = p.close(ctx); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	count := 0
	for _, h := range k.closed {
		if h == 53 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("pending close duplicated = %v", k.closed)
	}
}

func TestProfilePipeUnknownAcquisitionNeverClaimsCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k := &profilePipeKernelFixture{createFn: func() (uintptr, uintptr, error) { panic("unknown CreatePipe outcome") }}
	p, err := newProfilePipe(ctx, profilePipeTestProducer(), k, &profileSyncFixture{})
	if p == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("unknown acquisition = %v, %v", p, err)
	}
	if err = p.close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("unknown resources reclaimed = %v", err)
	}
}

func TestProfilePipeInvalidProducerRejectsBeforeAllocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k := &profilePipeKernelFixture{createFn: func() (uintptr, uintptr, error) { t.Fatal("invalid producer allocated a pipe"); return 0, 0, nil }}
	p, err := newProfilePipe(ctx, profilePipeProducerFixture{}, k, &profileSyncFixture{})
	if p != nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid producer = %v, %v", p, err)
	}
}
