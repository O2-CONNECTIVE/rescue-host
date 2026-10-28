package serviceready

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCanceledCloseRetainsConnectButIndependentlyClosesQueryRefs(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.listener.op.polls = 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	if err = p.Close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if k.listener.op.polls != 1 || k.listener.op.released || !k.listener.op.cancelled || k.listener.closes != 0 {
		t.Fatal("connect完了前に解放")
	}
	if k.svc.closes != 1 || k.proc.closes != 1 || k.scope.closes != 1 {
		t.Fatal("独立query refsの回収を妨げた")
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if !k.listener.op.released || k.listener.closes != 1 || k.svc.closes != 1 || k.scope.closes != 1 {
		t.Fatal("同じ参照を再回収しない")
	}
}
func TestConfirmRunningWaitsForKernelRunningAndRejectsLaterRegression(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	k.svc.onInspect = func() {
		checks++
		if checks == 2 {
			k.svc.value.state = 4
			k.svc.value.pid = 55
		}
	}
	if err = p.ConfirmRunning(testContext(t)); err != nil || checks < 2 {
		t.Fatal(err, checks)
	}
	k.svc.onInspect = nil
	k.svc.value.state = 2
	k.svc.value.pid = 999
	if err = p.Recheck(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("RUNNING後にpendingへ戻った", err)
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestEveryContextOrAdmissionFailureRevokesPlane(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(map[bool]string{false: "recheck", true: "confirm"}[confirm], func(t *testing.T) {
			c := config()
			k := fixture(c)
			f, _ := NewNative(c)
			f.api = k
			p, err := f.Prepare(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			cancel()
			if confirm {
				err = p.ConfirmRunning(ctx)
			} else {
				err = p.Recheck(ctx)
			}
			if err == nil {
				t.Fatal("取消ctx受理")
			}
			if err = p.Recheck(testContext(t)); err == nil {
				t.Fatal("取消後に復活")
			}
			k.listener.op.done = true
			if err = p.Close(testContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestTimedOutAdmissionRevokesAnAlreadyAdmittedRecheck(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	k.svc.onInspect = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}
	longResult := make(chan error, 1)
	longCtx := testContext(t)
	go func() { longResult <- p.Recheck(longCtx) }()
	<-entered
	var release sync.Once
	joined := false
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		if !joined {
			<-longResult
		}
	})
	short, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.ConfirmRunning(short) }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("admission失敗を受理")
		}
	case <-time.After(time.Second):
		release.Do(func() { close(gate) })
		<-result
		t.Fatal("ctx後もGo mutexを待った")
	}
	select {
	case err := <-longResult:
		joined = true
		t.Fatal("gate開放前に完了", err)
	default:
	}
	release.Do(func() { close(gate) })
	err = <-longResult
	joined = true
	if err == nil {
		t.Fatal("並行失効後のinflight検査が成功")
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestForeignThreadCloseKeepsFatalEvenWhenContextIsCanceled(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.thread = 8
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	if err = p.Close(ctx); !errors.Is(err, ErrFatal) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("既取消のforeign CloseがFatalを失う", err)
	}
	k.thread = 7
	k.listener.op.done = true
	if err = p.Close(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
}
func TestCloseCannotWaitPastCallerWhileRecheckOwnsAdmission(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	k.svc.onInspect = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}
	longResult := make(chan error, 1)
	longCtx := testContext(t)
	go func() { longResult <- p.Recheck(longCtx) }()
	<-entered
	var release sync.Once
	joined := false
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		if !joined {
			<-longResult
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.Close(ctx) }()
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnconfirmed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		release.Do(func() { close(gate) })
		<-result
		t.Fatal("Closeが他queryのGo mutexを待ち続けた")
	}
	select {
	case err := <-longResult:
		joined = true
		t.Fatal("gate開放前にquery終了", err)
	default:
	}
	release.Do(func() { close(gate) })
	err = <-longResult
	joined = true
	if err == nil {
		t.Fatal("Close後にinflight queryが成功")
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestPartialPrepareReturnsOwnerAndCloseReclaimsEachIndependentReference(t *testing.T) {
	for _, stage := range []string{"service", "listener"} {
		t.Run(stage, func(t *testing.T) {
			c := config()
			k := fixture(c)
			if stage == "service" {
				k.serviceErr = ErrDenied
			} else {
				k.listenerErr = ErrDenied
			}
			f, _ := NewNative(c)
			f.api = k
			p, err := f.Prepare(testContext(t))
			if p == nil || err == nil {
				t.Fatal(p, err)
			}
			if err = p.Recheck(testContext(t)); err == nil {
				t.Fatal("部分prepareを受理")
			}
			if err = p.Close(testContext(t)); err != nil {
				t.Fatal(err)
			}
			if k.scope.closes != 1 || k.svc.closes != 1 {
				t.Fatal("partial refs消失")
			}
			if stage == "listener" && (k.listener.closes != 1 || k.proc.closes != 1) {
				t.Fatal("listener partial消失")
			}
		})
	}
}
func TestFatalScopeCleanupDoesNotPreventIndependentReferenceCleanup(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.scope.closeErr = ErrFatal
	k.listener.op.done = true
	if err = p.Close(testContext(t)); !errors.Is(err, ErrFatal) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if k.svc.closes != 1 || k.proc.closes != 1 || k.listener.closes != 1 {
		t.Fatal("Fatal scopeが独立refs回収を妨げる")
	}
	k.scope.closeErr = nil
	if err = p.Close(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	if k.scope.closes != 2 || k.svc.closes != 1 {
		t.Fatal("同じscopeを再回収しない")
	}
}
