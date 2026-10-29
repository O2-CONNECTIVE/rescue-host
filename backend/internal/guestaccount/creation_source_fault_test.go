package guestaccount

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func creationFixture(t *testing.T, run func(*fixture, Plan, []byte) ([]byte, error)) (*Lifecycle, *fixture, *creationSource) {
	t.Helper()
	l, f := newFixture(t)
	p := l.plan
	deps := creationDependencies{preflight: creationCheckFixture{f}, create: func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) { return run(f, p, in) }}, nil
	}, inspect: func(b binding) (ownedAccountInspector, error) { return creationInspectorFixture{f}, nil }, cleanup: func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) { f.step("cleanup-native"); return sidResult(in), nil }}, nil
	}}
	c, err := newCreationSource(p, f.s.accountSeal, deps)
	if err != nil {
		t.Fatal(err)
	}
	l.sources.accounts = c
	return l, f, c
}

func TestCreationSourceIncompleteInspectorKeepsOwnedSIDAndRefusesDelete(t *testing.T) {
	l, f, _ := creationFixture(t, func(f *fixture, p Plan, in []byte) ([]byte, error) {
		b := p.binding
		b.sid = b.machine + "-2002"
		f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: b, presence: exact, disabled: true, complete: false}
		return creationResult(in, b.sid, true, true), nil
	})
	if err := l.Prepare(testContext(t)); err == nil {
		t.Fatal("不完全検査で準備成功")
	}
	if l.record.binding.sid != "S-1-5-21-10-20-30-2002" || f.stored.stage != ownedSID {
		t.Fatal("作成SIDを保持せず", l.record)
	}
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("完全検査なしに清掃成功")
	}
	calls := 0
	for _, step := range f.trace {
		if step == "cleanup-native" {
			calls++
		}
	}
	if calls != 1 || f.stored.stage >= deleteAttempt {
		t.Fatal("disable以外の変更", f.trace, f.stored.stage)
	}
}

func TestCreationSourceCannotAdoptSIDFromUnconfirmedOrChangedResult(t *testing.T) {
	for _, mode := range []string{"command-error", "truncated", "duplicate", "foreign-machine", "operator", "wrong-request", "panic"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			l, _, c := creationFixture(t, func(f *fixture, p Plan, in []byte) ([]byte, error) {
				calls++
				sid := p.binding.machine + "-2002"
				if mode == "foreign-machine" {
					sid = "S-1-5-21-1-2-3-2002"
				}
				if mode == "operator" {
					sid = p.binding.operator
				}
				out := creationResult(in, sid, true, true)
				switch mode {
				case "command-error":
					return out, ErrUnknown
				case "truncated":
					out = out[:len(out)-1]
				case "duplicate":
					out = bytes.Replace(out, []byte(`"disabled":true`), []byte(`"disabled":true,"disabled":true`), 1)
				case "wrong-request":
					out = bytes.Replace(out, []byte(`"generation":"1"`), []byte(`"generation":"2"`), 1)
				case "panic":
					panic("合成の作成失敗")
				}
				return out, nil
			})
			if err := l.Prepare(testContext(t)); err == nil {
				t.Fatal("不確定作成を成功扱い")
			}
			if l.record.binding.sid != "" {
				t.Fatal("不確定結果を来歴へ昇格")
			}
			if _, err := c.createDisabled(testContext(t), l.plan.binding); err == nil || calls != 1 {
				t.Fatal("盲再作成", calls, err)
			}
			if err := l.Close(testContext(t)); !errors.Is(err, ErrManual) {
				t.Fatal("SIDなしを不在扱い", err)
			}
		})
	}
}

func TestCreationSourceRetainsConfirmedSIDBeforeLateCancellationOrInspectorFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "enabled", "renamed", "inspector-error", "inspector-panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			l, _, c := creationFixture(t, func(f *fixture, p Plan, in []byte) ([]byte, error) {
				if mode == "cancel" {
					cancel()
				}
				return creationResult(in, p.binding.machine+"-2002", mode != "enabled", mode != "renamed"), nil
			})
			if mode == "inspector-error" {
				c.deps.inspect = func(binding) (ownedAccountInspector, error) { return nil, ErrUnconfigured }
			}
			if mode == "inspector-panic" {
				c.deps.inspect = func(binding) (ownedAccountInspector, error) { panic("合成の検査構成失敗") }
			}
			r, err := c.createDisabled(ctx, l.plan.binding)
			if err == nil || r.seal != l.sources.accountSeal || r.binding.sid != "S-1-5-21-10-20-30-2002" || c.binding != r.binding {
				t.Fatal("確認済SIDを喪失", r, err)
			}
		})
	}
}

func TestCreationSourceRejectsPreflightBeforeStartingAccountCommand(t *testing.T) {
	calls := 0
	l, f, _ := creationFixture(t, func(*fixture, Plan, []byte) ([]byte, error) { calls++; return nil, nil })
	f.fail["create-preflight"] = ErrDenied
	if err := l.Prepare(testContext(t)); err == nil || calls != 0 {
		t.Fatal("preflight失敗後に作成", calls, err)
	}
}

func TestCreationSourceUnknownInspectionCannotStartNewNativeCleanup(t *testing.T) {
	l, f, c := creationFixture(t, func(f *fixture, p Plan, in []byte) ([]byte, error) {
		b := p.binding
		b.sid = b.machine + "-2002"
		f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: b, presence: exact, disabled: true}
		return creationResult(in, b.sid, true, true), nil
	})
	f.fail["inspect"] = ErrUnknown
	if err := l.Prepare(testContext(t)); err == nil {
		t.Fatal("不明検査で準備成功")
	}
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("不明検査後に清掃成功")
	}
	for _, step := range f.trace {
		if step == "cleanup-native" {
			t.Fatal("unsafe queryの後に新しいnative cleanupを取得")
		}
	}
	f.fail["inspect"] = nil
	f.snapshot.complete = true
	if _, err := c.inspect(testContext(t), l.record.binding); err == nil {
		t.Fatal("不明検査を後続成功で復活")
	}
	if err := c.Close(testContext(t)); err == nil {
		t.Fatal("不明queryを回収済みにした")
	}
}

func TestCreationSourceCloseKeepsLateKnownSIDAndRequiresActualReturn(t *testing.T) {
	l, _, c := creationFixture(t, func(*fixture, Plan, []byte) ([]byte, error) {
		t.Fatal("差替え前の作成を実行")
		return nil, ErrUnknown
	})
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c.deps.create = func() (sidExecutor, error) {
		return &sidTestExecutor{
			run: func(in []byte) ([]byte, error) {
				close(entered)
				<-release
				out := creationResult(in, l.plan.binding.machine+"-2002", true, true)
				close(returned)
				return out, nil
			},
			close: func(context.Context) error {
				select {
				case <-returned:
					return nil
				default:
					return ErrCleanup
				}
			},
		}, nil
	}
	type result struct {
		r   accountReceipt
		err error
	}
	done := make(chan result, 1)
	ctx := testContext(t)
	go func() { r, err := c.createDisabled(ctx, l.plan.binding); done <- result{r, err} }()
	<-entered
	if err := c.Close(testContext(t)); err == nil {
		t.Fatal("未returnの作成を回収済み扱い")
	}
	close(release)
	r := <-done
	if r.err == nil || r.r.binding.sid != "S-1-5-21-10-20-30-2002" {
		t.Fatal("遅延成功で再admit/SID喪失", r)
	}
	if err := c.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.createDisabled(testContext(t), l.plan.binding); err == nil {
		t.Fatal("stop後の再作成")
	}
}

func TestCreationSourceHealthyInspectionRefusalStillContainsItsOwnSID(t *testing.T) {
	l, f, c := creationFixture(t, func(f *fixture, p Plan, in []byte) ([]byte, error) {
		return creationResult(in, p.binding.machine+"-2002", true, true), nil
	})
	f.fail["inspect"] = ErrDenied
	if err := l.Prepare(testContext(t)); err == nil {
		t.Fatal("拒否された検査で準備成功")
	}
	if err := l.Close(testContext(t)); err == nil {
		t.Fatal("拒否された検査で削除完了")
	}
	calls := 0
	for _, step := range f.trace {
		if step == "cleanup-native" {
			calls++
		}
	}
	if calls != 1 || f.stored.stage != ownedSID {
		t.Fatal("確定SIDのcontainmentを省略/削除へ進行", f.trace, f.stored.stage)
	}
	if err := c.Close(testContext(t)); err != nil {
		t.Fatal("回収済sourceをactor fatal扱い", err)
	}
	if l.state == Closed {
		t.Fatal("source Closeをaccount清掃完了へ転用")
	}
}

func TestCreationSourceInspectorPanicDoesNotResumeNativeWork(t *testing.T) {
	l, f, c := creationFixture(t, func(f *fixture, p Plan, in []byte) ([]byte, error) {
		return creationResult(in, p.binding.machine+"-2002", true, true), nil
	})
	f.panics["inspect"] = true
	if err := l.Prepare(testContext(t)); err == nil {
		t.Fatal("panic後に準備成功")
	}
	_ = l.Close(testContext(t))
	for _, step := range f.trace {
		if step == "cleanup-native" {
			t.Fatal("検査panic後にnative再開")
		}
	}
	if !c.inspectionUnknown.Load() {
		t.Fatal("検査panicをsource不明に保持しない")
	}
}
