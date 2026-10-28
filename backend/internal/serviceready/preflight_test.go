package serviceready

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// memory kernelだけでquery-only prefixとlistener側の呼出しを分けて観測する。
type preflightKernel struct {
	*fakeKernel
	serviceOpens, processOpens, listenerCreates int
	prepareError, processError                  error
}

func (k *preflightKernel) prepare(ctx context.Context, token uintptr) (preparedScope, error) {
	scope, err := k.fakeKernel.prepare(ctx, token)
	if k.prepareError != nil {
		return scope, k.prepareError
	}
	return scope, err
}
func (k *preflightKernel) openService(name string) (serviceRef, error) {
	k.serviceOpens++
	return k.fakeKernel.openService(name)
}
func (k *preflightKernel) openProcess(pid uint32) (processRef, error) {
	k.processOpens++
	process, err := k.fakeKernel.openProcess(pid)
	if k.processError != nil {
		return process, k.processError
	}
	return process, err
}
func (k *preflightKernel) createListener(name, sddl string) (listenerRef, error) {
	k.listenerCreates++
	return k.fakeKernel.createListener(name, sddl)
}
func preflightFixture(t *testing.T) (*Factory, *preflightKernel) {
	t.Helper()
	c := config()
	k := &preflightKernel{fakeKernel: fixture(c)}
	f, e := NewNative(c)
	if e != nil {
		t.Fatal(e)
	}
	f.api = k
	return f, k
}

func TestPreflightHoldsQueryOnlyEvidenceWithoutListenerOrRunning(t *testing.T) {
	f, k := preflightFixture(t)
	p, e := f.PreparePreflight(testContext(t))
	if e != nil || p == nil {
		t.Fatalf("START_PENDINGのquery-only証拠を保持しませんでした: %v", e)
	}
	if k.listenerCreates != 0 || k.listener.inspects != 0 || k.listener.op.polls != 0 {
		t.Fatal("Preflightがlistener作成/connectを始めました")
	}
	if k.serviceOpens != 1 || k.processOpens != 1 || k.prepareCalls != 1 {
		t.Fatal("同じquery-only所有参照を取得しませんでした")
	}
	binding, e := p.Binding(testContext(t))
	if e != nil || binding != (ProcessBinding{PID: 55, Creation: 42}) {
		t.Fatalf("保持したprocess generationを返しませんでした: %v", e)
	}
	if e := p.Recheck(testContext(t)); e != nil {
		t.Fatal(e)
	}
	if k.listenerCreates != 0 || k.serviceOpens != 1 || k.processOpens != 1 {
		t.Fatal("Binding/Recheckでlistenerやquery ownerを再生成しました")
	}
	if e := p.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	if k.scope.closes != 1 || k.svc.closes != 1 || k.proc.closes != 1 || k.listener.closes != 0 {
		t.Fatal("query-only所有物だけを回収しませんでした")
	}
}

func TestPreflightCleanupDistinguishesConfirmedFatalFromUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cleanup error
		pending bool
	}{
		{"confirmed fatal", ErrFatal, false},
		{"pending fatal", errors.Join(ErrFatal, ErrUnconfirmed), true},
		{"unknown joined fatal", errors.Join(ErrFatal, errors.New("unknown cleanup")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, k := preflightFixture(t)
			p, e := f.PreparePreflight(testContext(t))
			if e != nil {
				t.Fatal(e)
			}
			k.scope.closeErr = tc.cleanup
			e = p.Close(testContext(t))
			if !errors.Is(e, ErrFatal) || errors.Is(e, ErrUnconfirmed) != tc.pending {
				t.Fatalf("確認済みFatalと未確認を区別しませんでした: %v", e)
			}
			if k.svc.closes != 1 || k.proc.closes != 1 || k.listener.closes != 0 {
				t.Fatal("独立query refsの回収を妨げました")
			}
			k.scope.closeErr = nil
			if e := p.Close(testContext(t)); !errors.Is(e, ErrFatal) || errors.Is(e, ErrUnconfirmed) {
				t.Fatalf("回収完了後にFatalを忘れました: %v", e)
			}
			expected := 1
			if tc.pending {
				expected = 2
			}
			if k.scope.closes != expected {
				t.Fatal("確認済みscopeを二重回収したか未確認scopeを捨てました")
			}
			binding, e := p.Binding(testContext(t))
			if !errors.Is(e, ErrFatal) || binding != (ProcessBinding{}) {
				t.Fatal("回収後にquery bindingを復活させました")
			}
		})
	}
}

func TestPreflightAndPlaneRejectUnsafeQueryEvidenceBeforeListener(t *testing.T) {
	for _, listener := range []bool{false, true} {
		for _, change := range []func(*preflightKernel){
			func(k *preflightKernel) { k.svc.value.commandLine = `"C:\other.exe"` },
			func(k *preflightKernel) { k.svc.value.security.Protected = false },
			func(k *preflightKernel) { k.proc.value.security.Protected = false },
			func(k *preflightKernel) { k.proc.value.alive = false },
			func(k *preflightKernel) { k.proc.value.creation = 0 },
		} {
			f, k := preflightFixture(t)
			change(k)
			var owner interface{ Close(context.Context) error }
			var err error
			if listener {
				owner, err = f.Prepare(testContext(t))
			} else {
				owner, err = f.PreparePreflight(testContext(t))
			}
			if err == nil || owner == nil {
				t.Fatal("不正なservice/process証拠を受理しました")
			}
			if k.listenerCreates != 0 {
				t.Fatal("query検査を満たす前にlistenerを作成しました")
			}
			if e := owner.Close(testContext(t)); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestPreflightBindingRejectsChangedGenerationAndRemainsRevoked(t *testing.T) {
	for _, change := range []func(*preflightKernel){
		func(k *preflightKernel) { k.proc.value.creation++ },
		func(k *preflightKernel) { k.proc.value.pid++ },
		func(k *preflightKernel) { k.svc.value.state = 3 },
		func(k *preflightKernel) { k.svc.value.state = 4; k.svc.value.pid = 99 },
		func(k *preflightKernel) { k.proc.value.security.ACL[0].Mask = 0 },
	} {
		f, k := preflightFixture(t)
		p, e := f.PreparePreflight(testContext(t))
		if e != nil {
			t.Fatal(e)
		}
		change(k)
		if b, e := p.Binding(testContext(t)); e == nil || b != (ProcessBinding{}) {
			t.Fatal("変化したkernel証拠をBindingとして返しました")
		}
		k.proc.value = fixture(config()).proc.value
		k.svc.value = fixture(config()).svc.value
		if e := p.Recheck(testContext(t)); e == nil {
			t.Fatal("拒否したPreflightが復活しました")
		}
		if e := p.Close(testContext(t)); e != nil {
			t.Fatal(e)
		}
	}
}

func TestPreflightPartialAcquisitionKeepsSameOwner(t *testing.T) {
	for _, stage := range []string{"auth", "service", "process"} {
		f, k := preflightFixture(t)
		switch stage {
		case "auth":
			k.prepareError = ErrFatal
		case "service":
			k.serviceErr = ErrDenied
		case "process":
			k.processError = ErrDenied
		}
		p, e := f.PreparePreflight(testContext(t))
		if p == nil || e == nil {
			t.Fatal("部分取得したquery ownerを返しませんでした")
		}
		if b, e := p.Binding(testContext(t)); e == nil || b != (ProcessBinding{}) {
			t.Fatal("部分取得したPreflightを受理しました")
		}
		cleanup := p.Close(testContext(t))
		if stage == "auth" {
			if !errors.Is(cleanup, ErrFatal) || errors.Is(cleanup, ErrUnconfirmed) {
				t.Fatal(cleanup)
			}
		} else if cleanup != nil {
			t.Fatal(cleanup)
		}
		if k.scope.closes != 1 || stage != "auth" && k.svc.closes != 1 || stage == "process" && k.proc.closes != 1 || k.listenerCreates != 0 {
			t.Fatal("途中取得した実参照を保持・回収しませんでした")
		}
	}
}

func TestPreflightAdmissionFailureRevokesInflightBinding(t *testing.T) {
	f, k := preflightFixture(t)
	p, e := f.PreparePreflight(testContext(t))
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	k.svc.onInspect = func() { enteredOnce.Do(func() { close(entered) }); <-release }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		binding ProcessBinding
		err     error
	}
	first := make(chan outcome, 1)
	go func() { b, e := p.Binding(ctx); first <- outcome{b, e} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("先行Bindingがqueryへ入りませんでした")
	}
	short, stop := context.WithTimeout(ctx, 5*time.Millisecond)
	defer stop()
	if e := p.Recheck(short); e == nil {
		t.Fatal("admission timeoutを成功扱いしました")
	}
	unblock()
	done := <-first
	if done.err == nil || done.binding != (ProcessBinding{}) {
		t.Fatal("並行失効後のinflight検査がBindingを返しました")
	}
	if e := p.Recheck(testContext(t)); e == nil {
		t.Fatal("admission失敗で失効したscopeが復活しました")
	}
	if e := p.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestPreflightForeignThreadFatalIsPermanent(t *testing.T) {
	f, k := preflightFixture(t)
	p, e := f.PreparePreflight(testContext(t))
	if e != nil {
		t.Fatal(e)
	}
	k.thread++
	if b, e := p.Binding(testContext(t)); !errors.Is(e, ErrFatal) || b != (ProcessBinding{}) {
		t.Fatal("foreign threadからBindingを取り出しました")
	}
	k.thread--
	if e := p.Recheck(testContext(t)); !errors.Is(e, ErrFatal) {
		t.Fatal("Fatalを忘れました")
	}
	if e := p.Close(testContext(t)); !errors.Is(e, ErrFatal) {
		t.Fatal("回収でFatalを忘れました")
	}
}

func TestUnconfiguredOrCancelledPreflightNeverEntersNative(t *testing.T) {
	var f Factory
	if p, e := f.PreparePreflight(testContext(t)); p != nil || e != ErrConfig {
		t.Fatal("未構成factoryがPreflightを生成しました")
	}
	configured, k := preflightFixture(t)
	ctx, cancel := context.WithCancel(testContext(t))
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), ctx} {
		if p, e := configured.PreparePreflight(ctx); p != nil || e != ErrDenied {
			t.Fatal("有限かつ有効なctxなしでqueryへ入りました")
		}
	}
	if k.prepareCalls != 0 || k.serviceOpens != 0 || k.processOpens != 0 || k.listenerCreates != 0 {
		t.Fatal("不正入口でnative ownerを取得しました")
	}
	var p Preflight
	if binding, e := p.Binding(testContext(t)); e != ErrDenied || binding != (ProcessBinding{}) {
		t.Fatal("zero PreflightからBindingを作りました")
	}
	if p.Close(testContext(t)) != nil {
		t.Fatal("zero PreflightのCloseが所有物を捏造しました")
	}
}
