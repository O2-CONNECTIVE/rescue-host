package serviceready

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestServiceWitnessWaitsForSCMRunningWithoutListener(t *testing.T) {
	f, k := preflightFixture(t)
	w, err := f.PrepareServiceWitness(testContext(t))
	if err != nil || w == nil {
		t.Fatal(w, err)
	}
	checks := 0
	k.svc.onInspect = func() {
		checks++
		if checks == 2 {
			k.svc.value.state, k.svc.value.pid = 4, 55
		}
	}
	if err := w.ConfirmServiceRunning(testContext(t)); err != nil || checks < 2 {
		t.Fatal("held SCM RUNNINGを待ちませんでした", checks, err)
	}
	k.svc.onInspect = nil
	if err := w.RecheckService(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.listenerCreates != 0 || k.listener.inspects != 0 || k.listener.op.polls != 0 || k.serviceOpens != 1 || k.processOpens != 1 || k.prepareCalls != 1 {
		t.Fatal("同じquery owner以外を取得・使用しました")
	}
	if err := w.CloseService(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.scope.closes != 1 || k.svc.closes != 1 || k.proc.closes != 1 || k.listener.closes != 0 {
		t.Fatal("query-only所有物だけを回収しませんでした")
	}
}

func TestServiceWitnessCopiesCannotUseOrCloseCanonicalOwner(t *testing.T) {
	f, k := preflightFixture(t)
	k.svc.value.state, k.svc.value.pid = 4, 55
	w, err := f.PrepareServiceWitness(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.ConfirmServiceRunning(testContext(t)); err != nil {
		t.Fatal(err)
	}
	copy := *w
	checks := k.scope.checks
	if err := copy.ConfirmServiceRunning(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("コピーからSCM確認しました", err)
	}
	if err := copy.RecheckService(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("コピーからqueryを再検査しました", err)
	}
	if err := copy.CloseService(testContext(t)); !errors.Is(err, ErrDenied) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("コピーからcanonical ownerを回収済み扱いしました", err)
	}
	if checks != k.scope.checks || k.scope.closes != 0 || k.svc.closes != 0 || k.proc.closes != 0 {
		t.Fatal("コピーがcanonical query ownerを使用・回収しました")
	}
	if err := w.RecheckService(testContext(t)); err != nil {
		t.Fatal("コピーの拒否でcanonical ownerを失効しました", err)
	}
	if err := w.CloseService(testContext(t)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []*ServiceWitness{nil, new(ServiceWitness)} {
		if err := invalid.ConfirmServiceRunning(testContext(t)); err != ErrDenied {
			t.Fatal("未構成witnessを確認しました", err)
		}
		if err := invalid.RecheckService(testContext(t)); err != ErrDenied {
			t.Fatal("未構成witnessを再検査しました", err)
		}
		if err := invalid.CloseService(testContext(t)); err != nil {
			t.Fatal("未所有witnessの回収に失敗しました", err)
		}
	}
}

func TestServiceWitnessHasOnlyNarrowServiceMethods(t *testing.T) {
	type endpointPlane interface {
		Recheck(context.Context) error
		ConfirmRunning(context.Context) error
		Close(context.Context) error
	}
	if _, ok := any(new(ServiceWitness)).(endpointPlane); ok {
		t.Fatal("query witnessだけでendpoint readinessを主張できます")
	}
	typ := reflect.TypeOf(new(ServiceWitness))
	if typ.NumMethod() != 3 {
		t.Fatal("witnessの公開method数が増えました", typ.NumMethod())
	}
	for _, name := range []string{"ConfirmServiceRunning", "RecheckService", "CloseService"} {
		if _, ok := typ.MethodByName(name); !ok {
			t.Fatal("狭いservice methodがありません", name)
		}
	}
}

// Err/Doneの通知が遅れても、実deadlineを越えた成功を返さない。
type witnessDeadline struct {
	context.Context
	deadline time.Time
}

func (c *witnessDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestServiceWitnessRealDeadlineGuardsBeforeAndAfterQueries(t *testing.T) {
	for _, stage := range []string{"entry", "prepare", "confirm", "recheck"} {
		t.Run(stage, func(t *testing.T) {
			f, k := preflightFixture(t)
			ctx := &witnessDeadline{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
			expire := func() { ctx.deadline = time.Now().Add(-time.Second) }
			if stage == "entry" {
				expire()
			}
			if stage == "prepare" {
				k.svc.onInspect = expire
			}
			w, err := f.PrepareServiceWitness(ctx)
			if stage == "entry" {
				if w != nil || !errors.Is(err, ErrDenied) || k.prepareCalls != 0 {
					t.Fatal("実deadline後にquery ownerを取得しました", err)
				}
				return
			}
			if w == nil {
				t.Fatal("取得済みownerを失いました", err)
			}
			if stage != "prepare" {
				if err != nil {
					t.Fatal(err)
				}
				k.svc.value.state, k.svc.value.pid = 4, 55
				if stage == "recheck" {
					if err := w.ConfirmServiceRunning(testContext(t)); err != nil {
						t.Fatal(err)
					}
				}
				k.svc.onInspect = expire
				if stage == "confirm" {
					err = w.ConfirmServiceRunning(ctx)
				} else {
					err = w.RecheckService(ctx)
				}
			}
			if !errors.Is(err, ErrDenied) {
				t.Fatal("query中に実deadlineを越えて成功しました", err)
			}
			k.svc.onInspect = nil
			if err := w.ConfirmServiceRunning(testContext(t)); !errors.Is(err, ErrDenied) {
				t.Fatal("期限失敗した同じownerが復活しました", err)
			}
			if err := w.CloseService(testContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceWitnessQueryChangesStayRevoked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*preflightKernel)
		fatal  bool
	}{
		{"SCM PID", func(k *preflightKernel) { k.svc.value.pid++ }, false},
		{"SCM pending regression", func(k *preflightKernel) { k.svc.value.state = 2 }, false},
		{"SCM stopped", func(k *preflightKernel) { k.svc.value.state = 1 }, false},
		{"service type", func(k *preflightKernel) { k.svc.value.kind = 0x20 }, false},
		{"command line", func(k *preflightKernel) { k.svc.value.commandLine = `"C:\other.exe"` }, false},
		{"service ACL", func(k *preflightKernel) { k.svc.value.security.ACL[0].Mask = 0 }, false},
		{"process ACL", func(k *preflightKernel) { k.proc.value.security.Protected = false }, false},
		{"process PID", func(k *preflightKernel) { k.proc.value.pid++ }, false},
		{"process creation", func(k *preflightKernel) { k.proc.value.creation++ }, false},
		{"process exit", func(k *preflightKernel) { k.proc.value.alive = false }, false},
		{"query error", func(k *preflightKernel) { k.svc.inspectErr = ErrDenied }, false},
		{"subject", func(k *preflightKernel) { k.scope.err = ErrDenied }, false},
		{"subject fatal", func(k *preflightKernel) { k.scope.err = ErrFatal }, true},
		{"foreign thread", func(k *preflightKernel) { k.thread++ }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, k := preflightFixture(t)
			k.svc.value.state, k.svc.value.pid = 4, 55
			w, err := f.PrepareServiceWitness(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := w.ConfirmServiceRunning(testContext(t)); err != nil {
				t.Fatal(err)
			}
			tc.change(k)
			if err := w.RecheckService(testContext(t)); err == nil || errors.Is(err, ErrFatal) != tc.fatal {
				t.Fatal("変わったquery証拠を受理、またはFatalを縮小しました", err)
			}
			fresh := fixture(config())
			k.svc.value, k.proc.value = fresh.svc.value, fresh.proc.value
			k.svc.value.state, k.svc.value.pid = 4, 55
			k.thread, k.scope.err, k.svc.inspectErr = fresh.thread, nil, nil
			if err := w.ConfirmServiceRunning(testContext(t)); err == nil || errors.Is(err, ErrFatal) != tc.fatal {
				t.Fatal("失効した同じownerが復活しました", err)
			}
			if err := w.CloseService(testContext(t)); errors.Is(err, ErrFatal) != tc.fatal || errors.Is(err, ErrUnconfirmed) {
				t.Fatal("回収がFatal/unknownを取り違えました", err)
			}
		})
	}
}

func TestServiceWitnessPendingAndUnconfirmedRecheckCannotSucceed(t *testing.T) {
	for _, recheck := range []bool{false, true} {
		f, k := preflightFixture(t)
		w, err := f.PrepareServiceWitness(testContext(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		start := time.Now()
		if recheck {
			err = w.RecheckService(ctx)
		} else {
			err = w.ConfirmServiceRunning(ctx)
		}
		cancel()
		if !errors.Is(err, ErrDenied) || time.Since(start) > time.Second {
			t.Fatal("未確認/pendingを受理または期限内に戻りませんでした", err)
		}
		k.svc.value.state, k.svc.value.pid = 4, 55
		if err := w.ConfirmServiceRunning(testContext(t)); !errors.Is(err, ErrDenied) {
			t.Fatal("失敗したownerがRUNNINGで復活しました", err)
		}
		if err := w.CloseService(testContext(t)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServiceWitnessInvalidContextRevokesCanonicalOwner(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		for _, kind := range []string{"nil", "unbounded", "canceled", "expired"} {
			t.Run(kind+map[bool]string{false: " recheck", true: " confirm"}[confirm], func(t *testing.T) {
				f, k := preflightFixture(t)
				k.svc.value.state, k.svc.value.pid = 4, 55
				w, err := f.PrepareServiceWitness(testContext(t))
				if err != nil {
					t.Fatal(err)
				}
				if err := w.ConfirmServiceRunning(testContext(t)); err != nil {
					t.Fatal(err)
				}
				var ctx context.Context
				switch kind {
				case "unbounded":
					ctx = context.Background()
				case "canceled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(context.Background(), time.Hour)
					cancel()
				case "expired":
					ctx = &witnessDeadline{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
				}
				if confirm {
					err = w.ConfirmServiceRunning(ctx)
				} else {
					err = w.RecheckService(ctx)
				}
				if !errors.Is(err, ErrDenied) {
					t.Fatal("無効ctxを受理しました", err)
				}
				if err := w.RecheckService(testContext(t)); !errors.Is(err, ErrDenied) {
					t.Fatal("無効ctxの拒否後に復活しました", err)
				}
				if err := w.CloseService(testContext(t)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestServiceWitnessAdmissionFailureRevokesInflightQueryAndRetainsCleanup(t *testing.T) {
	for _, closeInstead := range []bool{false, true} {
		f, k := preflightFixture(t)
		k.svc.value.state, k.svc.value.pid = 4, 55
		w, err := f.PrepareServiceWitness(testContext(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.ConfirmServiceRunning(testContext(t)); err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer unblock()
		k.svc.onInspect = func() { enterOnce.Do(func() { close(entered) }); <-release }
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		first := make(chan error, 1)
		go func() { first <- w.RecheckService(ctx) }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("先行queryが入りませんでした")
		}
		short, stop := context.WithTimeout(ctx, 5*time.Millisecond)
		if closeInstead {
			err = w.CloseService(short)
			if !errors.Is(err, ErrUnconfirmed) || k.scope.closes != 0 || k.svc.closes != 0 || k.proc.closes != 0 {
				t.Fatal("進行中のquery参照を回収済み扱いしました", err)
			}
		} else {
			err = w.ConfirmServiceRunning(short)
			if !errors.Is(err, ErrDenied) {
				t.Fatal("admission失敗を受理しました", err)
			}
		}
		stop()
		unblock()
		if err := <-first; !errors.Is(err, ErrDenied) {
			t.Fatal("並行失効後の進行中queryが成功しました", err)
		}
		k.svc.onInspect = nil
		if err := w.RecheckService(testContext(t)); !errors.Is(err, ErrDenied) {
			t.Fatal("admission失敗後に復活しました", err)
		}
		if err := w.CloseService(testContext(t)); err != nil {
			t.Fatal(err)
		}
		if k.scope.closes != 1 || k.svc.closes != 1 || k.proc.closes != 1 {
			t.Fatal("同じpointerのquery参照を再回収しませんでした")
		}
	}
}

func TestServiceWitnessPartialAcquisitionKeepsOriginalCleanupOwner(t *testing.T) {
	for _, stage := range []string{"auth", "service", "process", "query"} {
		t.Run(stage, func(t *testing.T) {
			f, k := preflightFixture(t)
			switch stage {
			case "auth":
				k.prepareError = ErrFatal
			case "service":
				k.serviceErr = ErrDenied
			case "process":
				k.processError = ErrDenied
			case "query":
				k.svc.value.commandLine = `"C:\other.exe"`
			}
			w, err := f.PrepareServiceWitness(testContext(t))
			if w == nil || err == nil {
				t.Fatal("失敗時の部分取得ownerを保持しませんでした", err)
			}
			if err := w.ConfirmServiceRunning(testContext(t)); err == nil {
				t.Fatal("部分取得ownerでRUNNINGを確認しました")
			}
			cleanup := w.CloseService(testContext(t))
			if errors.Is(cleanup, ErrFatal) != (stage == "auth") || errors.Is(cleanup, ErrUnconfirmed) {
				t.Fatal(cleanup)
			}
			if k.scope.closes != 1 || stage != "auth" && k.svc.closes != 1 || (stage == "process" || stage == "query") && k.proc.closes != 1 || k.listenerCreates != 0 {
				t.Fatal("部分取得した参照を同じownerで回収しませんでした")
			}
		})
	}
}

func TestServiceWitnessCleanupRetainsUnknownAndFatal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cleanup error
		pending bool
		fatal   bool
	}{
		{"unknown", errors.New("unknown cleanup"), true, false},
		{"confirmed fatal", ErrFatal, false, true},
		{"pending fatal", errors.Join(ErrFatal, ErrUnconfirmed), true, true},
		{"unknown joined fatal", errors.Join(ErrFatal, errors.New("unknown cleanup")), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, k := preflightFixture(t)
			w, err := f.PrepareServiceWitness(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			k.scope.closeErr = tc.cleanup
			if err := w.CloseService(testContext(t)); errors.Is(err, ErrFatal) != tc.fatal || errors.Is(err, ErrUnconfirmed) != tc.pending {
				t.Fatal("確認済みFatalとunknownを区別しませんでした", err)
			}
			if k.svc.closes != 1 || k.proc.closes != 1 || k.listener.closes != 0 {
				t.Fatal("独立query refsの回収を妨げました")
			}
			k.scope.closeErr = nil
			if err := w.CloseService(testContext(t)); errors.Is(err, ErrFatal) != tc.fatal || errors.Is(err, ErrUnconfirmed) {
				t.Fatal("再回収がFatal/unknownを取り違えました", err)
			}
			expected := 1
			if tc.pending {
				expected = 2
			}
			if k.scope.closes != expected || k.svc.closes != 1 || k.proc.closes != 1 {
				t.Fatal("unknown参照を捨てたか確認済み参照を二重回収しました")
			}
			if err := w.RecheckService(testContext(t)); err == nil || errors.Is(err, ErrFatal) != tc.fatal {
				t.Fatal("回収後に受理が復活しました", err)
			}
		})
	}
}

func TestServiceWitnessForeignThreadCloseKeepsFatalAndCanonicalReferences(t *testing.T) {
	f, k := preflightFixture(t)
	w, err := f.PrepareServiceWitness(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.thread++
	if err := w.CloseService(nil); !errors.Is(err, ErrFatal) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("不正ctxのforeign CloseでFatalまたはunknownを失いました", err)
	}
	if k.scope.closes != 0 || k.svc.closes != 0 || k.proc.closes != 0 {
		t.Fatal("期限なしに回収しました")
	}
	k.thread--
	if err := w.CloseService(testContext(t)); !errors.Is(err, ErrFatal) || errors.Is(err, ErrUnconfirmed) {
		t.Fatal("元threadでの再回収がFatalを忘れました", err)
	}
	if k.scope.closes != 1 || k.svc.closes != 1 || k.proc.closes != 1 {
		t.Fatal("元pointerから保持参照を回収しませんでした")
	}
}

func TestServiceWitnessUnconfiguredOrInvalidEntryNeverQueries(t *testing.T) {
	for _, f := range []*Factory{nil, new(Factory)} {
		if w, err := f.PrepareServiceWitness(testContext(t)); w != nil || err != ErrConfig {
			t.Fatal("未構成factoryがwitnessを作りました", err)
		}
	}
	f, k := preflightFixture(t)
	canceled, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), canceled} {
		if w, err := f.PrepareServiceWitness(ctx); w != nil || err != ErrDenied {
			t.Fatal("無効ctxでquery ownerを作りました", err)
		}
	}
	if k.prepareCalls != 0 || k.serviceOpens != 0 || k.processOpens != 0 || k.listenerCreates != 0 {
		t.Fatal("無効入口でnative queryを呼びました")
	}
}
