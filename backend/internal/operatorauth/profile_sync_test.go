package operatorauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type profileSyncFixture struct {
	mu         sync.Mutex
	kinds      []profileSyncKind
	closed     int
	transferFn func(profileSyncKind, uintptr, []byte) (uint32, error)
	cancelFn   func(uintptr) error
	threadFn   func() (uintptr, error)
	closeFn    func(uintptr) error
}

type profileSyncCancelAfterCheck struct {
	context.Context
	calls    int
	done     chan struct{}
	deadline time.Time
}

func (c *profileSyncCancelAfterCheck) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *profileSyncCancelAfterCheck) Done() <-chan struct{}       { return c.done }
func (c *profileSyncCancelAfterCheck) Err() error {
	c.calls++
	if c.calls == 2 {
		close(c.done)
	}
	if c.calls >= 2 {
		return context.Canceled
	}
	return nil
}

func (f *profileSyncFixture) currentThread() (uintptr, error) {
	if f.threadFn != nil {
		return f.threadFn()
	}
	return 41, nil
}
func (f *profileSyncFixture) transfer(k profileSyncKind, h uintptr, b []byte) (uint32, error) {
	f.mu.Lock()
	f.kinds = append(f.kinds, k)
	f.mu.Unlock()
	if f.transferFn != nil {
		return f.transferFn(k, h, b)
	}
	return uint32(len(b)), nil
}
func (f *profileSyncFixture) cancel(h uintptr) error {
	if f.cancelFn != nil {
		return f.cancelFn(h)
	}
	return nil
}
func (f *profileSyncFixture) closeThread(h uintptr) error {
	if f.closeFn != nil {
		return f.closeFn(h)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func TestProfileSyncPanicRetainsUnknownWorkerAndResources(t *testing.T) {
	for _, stage := range []string{"thread", "transfer", "close", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			f := &profileSyncFixture{}
			started := make(chan struct{})
			switch stage {
			case "thread":
				f.threadFn = func() (uintptr, error) { panic("unknown thread reference") }
			case "transfer":
				f.transferFn = func(profileSyncKind, uintptr, []byte) (uint32, error) { panic("unknown I/O return") }
			case "close":
				f.closeFn = func(uintptr) error { panic("unknown close") }
			case "cancel":
				f.transferFn = func(profileSyncKind, uintptr, []byte) (uint32, error) { close(started); select {} }
				f.cancelFn = func(uintptr) error { panic("unknown cancel") }
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			o, err := startProfileSync(ctx, f, profileSyncRead, 77, []byte("frame"))
			if err != nil {
				t.Fatal(err)
			}
			if stage == "cancel" {
				<-started
			}
			short, done := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer done()
			_, err = o.wait(short, time.Second, stage == "cancel")
			if !errors.Is(err, ErrUnconfirmed) {
				t.Fatalf("panic reclaimed = %v", err)
			}
			if err = o.release(); !errors.Is(err, ErrUnconfirmed) {
				t.Fatalf("panic release = %v", err)
			}
		})
	}
}

func TestProfileSyncOriginalCancellationIsStickyAcrossRecoveryContext(t *testing.T) {
	for _, kind := range []profileSyncKind{profileSyncRead, profileSyncWrite} {
		started, returned := make(chan struct{}), make(chan struct{})
		attempts := 0
		f := &profileSyncFixture{
			transferFn: func(profileSyncKind, uintptr, []byte) (uint32, error) { close(started); <-returned; return 5, nil },
			cancelFn: func(uintptr) error {
				attempts++
				if attempts == 2 {
					close(returned)
				}
				return errors.New("no matching request yet")
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		o, err := startProfileSync(ctx, f, kind, 77, []byte("frame"))
		if err != nil {
			t.Fatal(err)
		}
		<-started
		cancel()
		recovery, done := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err = o.wait(recovery, time.Second, false)
		done()
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("sticky cancellation = %v", err)
		}
		if err := o.release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProfileSyncReadAndWriteReturnBeforeThreadRelease(t *testing.T) {
	for _, kind := range []profileSyncKind{profileSyncRead, profileSyncWrite} {
		f := &profileSyncFixture{}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		o, err := startProfileSync(ctx, f, kind, 77, []byte("frame"))
		if err != nil {
			t.Fatal(err)
		}
		n, err := o.wait(ctx, time.Second, false)
		cancel()
		if err != nil || n != 5 {
			t.Fatalf("return = %d, %v", n, err)
		}
		if err := o.release(); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		if len(f.kinds) != 1 || f.kinds[0] != kind || f.closed != 1 {
			t.Fatalf("calls = %v, closed = %d", f.kinds, f.closed)
		}
		f.mu.Unlock()
	}
}

func TestProfileSyncStopBeforeStartRetainsOwnerUntilThreadSetupReturns(t *testing.T) {
	for _, kind := range []profileSyncKind{profileSyncRead, profileSyncWrite} {
		setup, resume := make(chan struct{}), make(chan struct{})
		f := &profileSyncFixture{threadFn: func() (uintptr, error) { close(setup); <-resume; return 41, nil }}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		o, err := startProfileSync(ctx, f, kind, 77, []byte("frame"))
		if err != nil {
			t.Fatal(err)
		}
		<-setup
		cancel()
		if _, err = o.wait(ctx, time.Second, true); !errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("setup unknown = %v", err)
		}
		if err = o.release(); !errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("premature release = %v", err)
		}
		close(resume)
		recovery, done := context.WithTimeout(context.Background(), time.Second)
		_, err = o.wait(recovery, time.Second, false)
		done()
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("stopped setup = %v", err)
		}
		if err = o.release(); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		if len(f.kinds) != 0 || f.closed != 1 {
			t.Fatalf("start-after-stop: calls = %v, closed = %d", f.kinds, f.closed)
		}
		f.mu.Unlock()
	}
}

func TestProfileSyncWaitsForCancellationHandshakeAfterRealReturn(t *testing.T) {
	for _, kind := range []profileSyncKind{profileSyncRead, profileSyncWrite} {
		started, returned := make(chan struct{}), make(chan struct{})
		cancelEntered, cancelReturn := make(chan struct{}), make(chan struct{})
		var request sync.Once
		f := &profileSyncFixture{
			transferFn: func(profileSyncKind, uintptr, []byte) (uint32, error) { close(started); <-returned; return 5, nil },
			cancelFn: func(uintptr) error {
				request.Do(func() { close(cancelEntered); close(returned) })
				<-cancelReturn
				return nil
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		o, err := startProfileSync(ctx, f, kind, 77, []byte("frame"))
		if err != nil {
			t.Fatal(err)
		}
		<-started
		short, done := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err = o.wait(short, time.Second, true)
		done()
		<-cancelEntered
		if !errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("cancel unknown = %v", err)
		}
		if err = o.release(); !errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("cancel pending release = %v", err)
		}
		f.mu.Lock()
		if f.closed != 0 {
			t.Fatalf("thread closed during cancellation = %d", f.closed)
		}
		f.mu.Unlock()
		close(cancelReturn)
		_, err = o.wait(ctx, time.Second, false)
		cancel()
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("handshake result = %v", err)
		}
		if err = o.release(); err != nil {
			t.Fatal(err)
		}
		// A late cancellation must never target an OS thread returned to Go.
		recovery, done := context.WithTimeout(context.Background(), time.Second)
		_, err = o.wait(recovery, time.Second, true)
		done()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sticky late stop = %v", err)
		}
	}
}

func TestProfileSyncNativeRejectsUnboundedContextBeforeWorker(t *testing.T) {
	o, err := startNativeProfileSync(context.Background(), profileSyncWrite, 77, []byte("frame"))
	if o != nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("unbounded native start = %v, %v", o, err)
	}
}

func TestProfileSyncLateStopDeniesSuccessWithoutCancelingReusedThread(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		f := &profileSyncFixture{cancelFn: func(uintptr) error { t.Error("late stop canceled a released OS thread"); return nil }}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		o, err := startProfileSync(ctx, f, profileSyncWrite, 77, []byte("frame"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = o.wait(ctx, time.Second, false); err != nil {
			t.Fatal(err)
		}
		if !explicit {
			cancel()
		}
		recovery, done := context.WithTimeout(context.Background(), time.Second)
		_, err = o.wait(recovery, time.Second, explicit)
		done()
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late stop success = %v", err)
		}
		if err = o.release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProfileSyncWaitExitRecordsStopBeforeReturningUnknown(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	f := &profileSyncFixture{transferFn: func(profileSyncKind, uintptr, []byte) (uint32, error) { close(started); <-finish; return 5, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	o, err := startProfileSync(ctx, f, profileSyncWrite, 77, []byte("frame"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	waitCtx := &profileSyncCancelAfterCheck{Context: context.Background(), done: make(chan struct{}), deadline: time.Now().Add(time.Second)}
	if _, err = o.wait(waitCtx, time.Second, false); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("cancel between checks = %v", err)
	}
	close(finish)
	_, err = o.wait(ctx, time.Second, false)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("recovery resurrected success = %v", err)
	}
	if err = o.release(); err != nil {
		t.Fatal(err)
	}
}

func TestProfileSyncCompletedWaitRechecksItsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	o, err := startProfileSync(ctx, &profileSyncFixture{}, profileSyncWrite, 77, []byte("frame"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.wait(ctx, time.Second, false); err != nil {
		t.Fatal(err)
	}
	waitCtx := &profileSyncCancelAfterCheck{Context: context.Background(), done: make(chan struct{}), deadline: time.Now().Add(time.Second)}
	_, err = o.wait(waitCtx, time.Second, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("completed wait ignored cancellation = %v", err)
	}
	if err = o.release(); err != nil {
		t.Fatal(err)
	}
}

func TestProfileSyncThreadAcquisitionPreservesOnlyValidPartialReferences(t *testing.T) {
	partialErr := errors.New("partial thread acquisition")
	for _, tc := range []struct {
		name       string
		thread     uintptr
		err        error
		wantErr    error
		wantClosed int
	}{
		{"invalid sentinel", ^uintptr(0), nil, ErrDenied, 0},
		{"valid partial reference", 41, partialErr, partialErr, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &profileSyncFixture{threadFn: func() (uintptr, error) { return tc.thread, tc.err }}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			o, err := startProfileSync(ctx, f, profileSyncRead, 77, []byte("frame"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = o.wait(ctx, time.Second, false)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("acquisition = %v", err)
			}
			if err = o.release(); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.closed != tc.wantClosed || len(f.kinds) != 0 {
				t.Fatalf("close = %d, transfers = %v", f.closed, f.kinds)
			}
		})
	}
}
