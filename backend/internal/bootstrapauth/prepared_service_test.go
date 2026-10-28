package bootstrapauth

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// 既存の memory kernel fixture を使い、SCM・peer・pipe 境界は未使用を観測します。
type preparingKernel struct {
	*fakeKernel
	pid              uint32
	forbiddenQueries int
	onCurrentImage   func()
	imageError       error
}

func (k *preparingKernel) currentPID() uint32 { k.markQuery(); return k.pid }
func (k *preparingKernel) currentImage() (string, error) {
	path, err := k.fakeKernel.currentImage()
	if k.onCurrentImage != nil {
		k.onCurrentImage()
	}
	if k.imageError != nil {
		return "", k.imageError
	}
	return path, err
}

func (k *preparingKernel) openService(string) (serviceHandle, error) {
	k.forbiddenQueries++
	return nil, ErrDenied
}
func (k *preparingKernel) openProcess(uint32) (processHandle, error) {
	k.forbiddenQueries++
	return nil, ErrDenied
}
func (k *preparingKernel) inspectPipe(uintptr) (pipeState, error) {
	k.forbiddenQueries++
	return pipeState{}, ErrDenied
}
func preparingFixture(t *testing.T) (*Factory, *preparingKernel) {
	t.Helper()
	p := testProfile()
	api := &preparingKernel{fakeKernel: newFake(p), pid: 9}
	api.currentPath = p.SupervisorImage.Path
	api.service.state = 2
	f, e := NewNativeFactory(p)
	if e != nil {
		t.Fatal(e)
	}
	f.api = api
	return f, api
}
func TestPrepareServiceChecksSelfBeforeRunningWithoutSCM(t *testing.T) {
	f, api := preparingFixture(t)
	prepared, e := f.PrepareService(authContext(t), 73)
	if e != nil || prepared == nil {
		t.Fatalf("RUNNING 前の自主体を確認できません: %v", e)
	}
	if api.forbiddenQueries != 0 {
		t.Fatal("準備を SCM/peer/pipe 認証で代用しました")
	}
	if len(api.files) != 2 {
		t.Fatal("固定 supervisor image の ancestor と leaf を保持しませんでした")
	}
	if e := prepared.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	for _, file := range api.files {
		if file.closes != 1 {
			t.Fatal("保持した image handle を回収しませんでした")
		}
	}
	if api.service.closes != 0 || api.process.closes != 0 {
		t.Fatal("準備外の service/process を取得または回収しました")
	}
}

func TestPreparedServiceRecheckBoundsAdmissionAndRejectsChangedSelf(t *testing.T) {
	f, api := preparingFixture(t)
	prepared, e := f.PrepareService(authContext(t), 73)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := prepared.Recheck(ctx); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	for _, file := range api.files {
		file.onRead = func() { enteredOnce.Do(func() { close(entered) }); <-release }
	}
	first := make(chan error, 1)
	go func() { first <- prepared.Recheck(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("先行 Recheck が image 検査へ進みませんでした")
	}
	waitingCtx, stopWaiting := context.WithCancel(ctx)
	waiting := make(chan error, 1)
	go func() { waiting <- prepared.Recheck(waitingCtx) }()
	stopWaiting()
	select {
	case err := <-waiting:
		if err != ErrDenied {
			t.Fatalf("待機の cancel を拒否しませんでした: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Recheck の admission 待機が context で終了しませんでした")
	}
	unblock()
	if e := <-first; e != nil {
		t.Fatal(e)
	}
	api.pid++
	if e := prepared.Recheck(ctx); e != ErrDenied {
		t.Fatalf("current PID の変更を受理しました: %v", e)
	}
	api.pid--
	if e := prepared.Recheck(ctx); e != ErrDenied {
		t.Fatal("拒否した準備 scope を再利用しました")
	}
	if e := prepared.Close(ctx); e != nil {
		t.Fatal(e)
	}
	if api.forbiddenQueries != 0 {
		t.Fatal("Recheck が SCM/peer/pipe に依存しました")
	}
}

func TestPreparedServiceDenialsRetainPartialOwnership(t *testing.T) {
	for name, change := range map[string]func(*preparingKernel){
		"current PID":   func(a *preparingKernel) { a.pid = 0 },
		"current image": func(a *preparingKernel) { a.currentPath = `C:\other.exe` },
		"borrowed user": func(a *preparingKernel) { a.borrowed = &identity{user: guest, kind: 1} },
		"borrowed type": func(a *preparingKernel) {
			a.borrowed = &identity{user: user, kind: 2, groups: []group{{sid: service, attributes: 4}}}
		},
		"borrowed deny-only": func(a *preparingKernel) {
			a.borrowed = &identity{user: user, kind: 1, groups: []group{{sid: service, attributes: 0x14}}}
		},
		"borrowed other groups": func(a *preparingKernel) {
			a.borrowed = &identity{user: user, kind: 1, groups: []group{{sid: service, attributes: 4}, {sid: operator, attributes: 4}}}
		},
		"partial open": func(a *preparingKernel) { a.fileError = ErrDenied },
	} {
		t.Run(name, func(t *testing.T) {
			f, a := preparingFixture(t)
			change(a)
			prepared, e := f.PrepareService(authContext(t), 73)
			if prepared == nil || e != ErrDenied {
				t.Fatalf("不一致または部分取得を拒否し所有物を返しませんでした: %v", e)
			}
			if prepared.Recheck(authContext(t)) != ErrDenied {
				t.Fatal("失敗した準備を再利用しました")
			}
			if e := prepared.Close(authContext(t)); e != nil {
				t.Fatal(e)
			}
			for _, file := range a.files {
				if file.closes != 1 {
					t.Fatal("部分取得した image を失いました")
				}
			}
			if a.forbiddenQueries != 0 {
				t.Fatal("自主体の拒否に SCM/peer/pipe を使いました")
			}
		})
	}
}

func TestPrepareServiceRejectsDirtySubjectBeforeOpeningImage(t *testing.T) {
	for name, change := range map[string]func(*preparingKernel){
		"thread token": func(a *preparingKernel) { a.dirty = true },
		"primary user": func(a *preparingKernel) { a.identity.user = guest },
	} {
		t.Run(name, func(t *testing.T) {
			f, a := preparingFixture(t)
			change(a)
			if prepared, e := f.PrepareService(authContext(t), 73); prepared != nil || e != ErrFatal {
				t.Fatal("不明な自主体を準備として返しました")
			}
			if len(a.files) != 0 || a.forbiddenQueries != 0 {
				t.Fatal("自主体の確認前に file または service を開きました")
			}
		})
	}
}

func TestPreparedServiceRecheckRejectsBorrowedTokenAndImageMutation(t *testing.T) {
	for name, change := range map[string]func(*preparingKernel){
		"borrowed token":     func(a *preparingKernel) { a.borrowed = &identity{user: guest, kind: 1} },
		"image path":         func(a *preparingKernel) { a.currentPath = `C:\other.exe` },
		"held file identity": func(a *preparingKernel) { a.files[1].value.id = [16]byte{2} },
		"held file ACL":      func(a *preparingKernel) { a.files[1].value.acl = nil },
		"held file links":    func(a *preparingKernel) { a.files[1].value.links = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			f, a := preparingFixture(t)
			prepared, e := f.PrepareService(authContext(t), 73)
			if e != nil {
				t.Fatal(e)
			}
			change(a)
			if e := prepared.Recheck(authContext(t)); e != ErrDenied {
				t.Fatalf("準備後の変更を受理しました: %v", e)
			}
			if e := prepared.Close(authContext(t)); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestPreparedServiceRetainsPoisonAndUnconfirmedCleanup(t *testing.T) {
	f, a := preparingFixture(t)
	prepared, e := f.PrepareService(authContext(t), 73)
	if e != nil {
		t.Fatal(e)
	}
	a.id++
	a.files[0].fail = true
	before := a.queryCalls
	e = prepared.Close(authContext(t))
	if !errors.Is(e, ErrFatal) || !errors.Is(e, ErrUnconfirmed) {
		t.Fatalf("foreign thread と未回収を保持しませんでした: %v", e)
	}
	if a.queryCalls != before {
		t.Fatal("foreign thread の回収で認証を再 query しました")
	}
	a.files[0].fail = false
	if e := prepared.Close(authContext(t)); !errors.Is(e, ErrFatal) || errors.Is(e, ErrUnconfirmed) {
		t.Fatalf("回収後に poison を忘れました: %v", e)
	}
	if prepared.Recheck(authContext(t)) != ErrFatal {
		t.Fatal("poison 済み準備を認証として復活させました")
	}
	if a.files[0].closes != 2 || a.files[1].closes != 1 {
		t.Fatal("失敗参照だけを再回収しませんでした")
	}
}

func TestPreparedServiceRechecksSelfAfterHash(t *testing.T) {
	f, a := preparingFixture(t)
	reads := 0
	a.onRead = func() {
		reads++
		if reads == 4 {
			a.pid++
		}
	}
	prepared, e := f.PrepareService(authContext(t), 73)
	if prepared == nil || e != ErrDenied {
		t.Fatalf("hash 中の自主体の変更を受理しました: %v", e)
	}
	if e := prepared.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestPreparedServiceRejectsCancelledAndUnconfiguredInputsWithoutNativeQueries(t *testing.T) {
	ctx, cancel := context.WithCancel(authContext(t))
	cancel()
	f, a := preparingFixture(t)
	for _, ctx := range []context.Context{nil, context.Background(), ctx} {
		if prepared, e := f.PrepareService(ctx, 73); prepared != nil || e != ErrDenied {
			t.Fatal("有限の有効 context がない準備を受理しました")
		}
	}
	for _, h := range []uintptr{0, ^uintptr(0), ^uintptr(0) - 3} {
		if prepared, e := f.PrepareService(authContext(t), h); prepared != nil || e != ErrDenied {
			t.Fatal("借用 primary token の null/pseudo handle を受理しました")
		}
	}
	if a.queryCalls != 0 || a.threadCalls != 0 {
		t.Fatal("不正な入口から native query へ進みました")
	}
	var zero Factory
	if prepared, e := zero.PrepareService(authContext(t), 73); prepared != nil || e != ErrConfig {
		t.Fatal("未構成の factory が native 準備へ進みました")
	}
	var empty PreparedService
	if empty.Recheck(authContext(t)) != ErrClosed || empty.Close(authContext(t)) != nil {
		t.Fatal("zero scope の拒否と回収が不正です")
	}
}

func TestPreparedServiceDoesNotWeakenRunningAuthentication(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	a.currentPath = p.SupervisorImage.Path
	a.service.pid = 9
	a.service.state = 2
	f, e := NewNativeFactory(p)
	if e != nil {
		t.Fatal(e)
	}
	f.api = a
	prepared, e := f.PrepareService(authContext(t), 73)
	if e != nil {
		t.Fatal(e)
	}
	strong, e := f.VerifyService(authContext(t), 73)
	if e != ErrDenied || strong == nil {
		t.Fatalf("RUNNING 前の自主体を ServiceScope として受理しました: %v", e)
	}
	if e := strong.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	a.currentPath = p.ChildImage.Path
	a.service.pid = 42
	peer, e := f.Authenticate(authContext(t), 99)
	if e != ErrDenied || peer == nil {
		t.Fatalf("RUNNING 前の発信元を Peer として受理しました: %v", e)
	}
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if e := prepared.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestPreparedServicePartialFailureRetainsFatalOverCancellation(t *testing.T) {
	f, a := preparingFixture(t)
	ctx, cancel := context.WithCancel(authContext(t))
	a.onRead = func() { a.dirty = true; cancel() }
	prepared, e := f.PrepareService(ctx, 73)
	if prepared == nil || e != ErrFatal {
		t.Fatalf("部分取得後の dirty thread を cancel の通常拒否へ縮小しました: %v", e)
	}
	before := a.queryCalls
	if e := prepared.Close(authContext(t)); e != ErrFatal {
		t.Fatalf("部分取得の回収後に fatal を忘れました: %v", e)
	}
	if a.queryCalls != before {
		t.Fatal("poison 済み準備の回収で再認証しました")
	}
	for _, file := range a.files {
		if file.closes != 1 {
			t.Fatal("部分取得した image handle を保持・回収しませんでした")
		}
	}
}

func TestCheckServiceCallerRejectsBeforeMovingToServiceOwner(t *testing.T) {
	for name, change := range map[string]func(*preparingKernel){
		"clean":                func(*preparingKernel) {},
		"thread token":         func(a *preparingKernel) { a.dirty = true },
		"primary user":         func(a *preparingKernel) { a.identity.user = guest },
		"disabled service SID": func(a *preparingKernel) { a.identity.groups[0].attributes = 0 },
		"zero PID":             func(a *preparingKernel) { a.pid = 0 },
		"wrong path":           func(a *preparingKernel) { a.currentPath = `C:\other.exe` },
		"path query failed":    func(a *preparingKernel) { a.imageError = ErrDenied },
		"dirty after path":     func(a *preparingKernel) { a.onCurrentImage = func() { a.dirty = true } },
	} {
		t.Run(name, func(t *testing.T) {
			f, a := preparingFixture(t)
			change(a)
			expected := ErrDenied
			switch name {
			case "clean":
				expected = nil
			case "thread token", "primary user", "disabled service SID", "dirty after path":
				expected = ErrFatal
			}
			if err := f.CheckServiceCaller(authContext(t)); err != expected {
				t.Fatalf("元 caller の guard 判定が違います: %v", err)
			}
			if len(a.files) != 0 || a.forbiddenQueries != 0 || a.service.closes != 0 || a.process.closes != 0 {
				t.Fatal("拒否専用 guard が owned handle または SCM/peer/pipe を使用しました")
			}
		})
	}
}

func TestCheckServiceCallerRequiresFiniteContextBeforeQueries(t *testing.T) {
	f, a := preparingFixture(t)
	cancelled, cancel := context.WithCancel(authContext(t))
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), cancelled} {
		if err := f.CheckServiceCaller(ctx); err != ErrDenied {
			t.Fatal("有限かつ有効な context のない guard を受理しました")
		}
	}
	if a.queryCalls != 0 || a.threadCalls != 0 {
		t.Fatal("不正 context の guard が native query へ進みました")
	}
	var zero Factory
	if zero.CheckServiceCaller(authContext(t)) != ErrConfig {
		t.Fatal("未構成 guard が元 caller の query へ進みました")
	}
}

func TestCheckServiceCallerCancellationCannotHideDirtyOriginalThread(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(strconv.FormatBool(dirty), func(t *testing.T) {
			f, a := preparingFixture(t)
			ctx, cancel := context.WithCancel(authContext(t))
			defer cancel()
			a.onCurrentImage = func() { a.dirty = dirty; cancel() }
			expected := ErrDenied
			if dirty {
				expected = ErrFatal
			}
			if err := f.CheckServiceCaller(ctx); err != expected {
				t.Fatalf("元 caller の cancel/dirty の判定が違います: %v", err)
			}
			if len(a.files) != 0 || a.forbiddenQueries != 0 {
				t.Fatal("元 caller の拒否で owned handle を開きました")
			}
		})
	}
}

func TestCheckServiceCallerSuccessCannotReplaceFullImagePreparation(t *testing.T) {
	p := testProfile()
	p.SupervisorImage.SHA256[0] ^= 1
	a := &preparingKernel{fakeKernel: newFake(p), pid: 9}
	a.currentPath = p.SupervisorImage.Path
	f, e := NewNativeFactory(p)
	if e != nil {
		t.Fatal(e)
	}
	f.api = a
	if e := f.CheckServiceCaller(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if len(a.files) != 0 {
		t.Fatal("guard が file を開いて image の完全検証を代用しました")
	}
	prepared, e := f.PrepareService(authContext(t), 73)
	if e != ErrDenied || prepared == nil {
		t.Fatalf("guard の成功で不一致の image hash を省略しました: %v", e)
	}
	if e := prepared.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}
