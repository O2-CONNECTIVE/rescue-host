package guestaccount

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type sidTestExecutor struct {
	run   func([]byte) ([]byte, error)
	close func(context.Context) error
}

func (e *sidTestExecutor) execute(_ context.Context, in []byte) ([]byte, error) { return e.run(in) }
func (e *sidTestExecutor) Close(ctx context.Context) error {
	if e.close != nil {
		return e.close(ctx)
	}
	return nil
}

func TestSIDCleanupTargetsOwnedSIDAfterNameReplacement(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	owned := l.record.binding
	otherSID := owned.machine + "-2000"
	accounts := map[string]bool{owned.sid: false, otherSID: false}
	var operations []accountKind
	factory := func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) {
			// fake OS は同名replacement後もSIDだけを対象とする。
			var op accountKind
			if string(in) == string(sidRequest(owned, disabled)) {
				op = disabled
			} else if string(in) == string(sidRequest(owned, deleted)) {
				op = deleted
			} else {
				t.Fatal("別bindingの操作", string(in))
			}
			operations = append(operations, op)
			if op == disabled {
				accounts[owned.sid] = true
			} else {
				delete(accounts, owned.sid)
			}
			return sidResult(in), nil
		}}, nil
	}
	a, err := newSIDSource(l.plan, owned, f.s.accountSeal, f, factory)
	if err != nil {
		t.Fatal(err)
	}
	l.sources.accounts = a
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || operations[0] != disabled || operations[1] != deleted {
		t.Fatal(operations)
	}
	if _, ok := accounts[owned.sid]; ok || accounts[otherSID] {
		t.Fatal("SID以外のaccountを変更", accounts)
	}
	if _, ok := accounts[otherSID]; !ok {
		t.Fatal("別SIDを削除")
	}
}

func TestSIDUncertainMutationBurnsAttemptAndCannotProveAbsence(t *testing.T) {
	for _, mode := range []string{"error", "wrong-binding", "extra-frame", "duplicate", "truncated", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			l, f := newFixture(t)
			if err := l.Prepare(testContext(t)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			calls := 0
			a, err := newSIDSource(l.plan, l.record.binding, f.s.accountSeal, f, func() (sidExecutor, error) {
				return &sidTestExecutor{run: func(in []byte) ([]byte, error) {
					calls++
					out := sidResult(in)
					switch mode {
					case "error":
						return out, ErrUnknown
					case "wrong-binding":
						out = bytes.Replace(out, []byte(`"generation":"1"`), []byte(`"generation":"2"`), 1)
					case "extra-frame":
						out = append(out, out...)
					case "duplicate":
						out = bytes.Replace(out, []byte(`"outcome":"ok"`), []byte(`"outcome":"ok","outcome":"ok"`), 1)
					case "truncated":
						out = out[:len(out)-1]
					case "cancel":
						cancel()
					case "panic":
						panic("合成の実行失敗")
					}
					return out, nil
				}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.disable(ctx, l.record.binding); !errors.Is(err, ErrUnknown) {
				t.Fatal(err)
			}
			if _, err = a.disable(testContext(t), l.record.binding); !errors.Is(err, ErrUnknown) {
				t.Fatal("不明操作の再実行", err)
			}
			if _, err = a.inspect(testContext(t), l.record.binding); !errors.Is(err, ErrUnknown) {
				t.Fatal("不明をsnapshotへ昇格", err)
			}
			if calls != 1 || len(a.owners) != 1 {
				t.Fatal("所有参照を喪失/再dispatch", calls, len(a.owners))
			}
		})
	}
}

func TestSIDSourceCannotInventCompleteInspectionOrCreation(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a, err := newSIDSource(l.plan, l.record.binding, f.s.accountSeal, f, func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) { calls++; return sidResult(in), nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.createDisabled(testContext(t), l.plan.binding); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	if _, err = a.disable(testContext(t), l.record.binding); err != nil {
		t.Fatal(err)
	}
	if _, err = a.inspect(testContext(t), l.record.binding); err != nil {
		t.Fatal(err)
	}
	f.snapshot.complete = false
	if r, err := a.inspect(testContext(t), l.record.binding); err != nil || r.complete {
		t.Fatal("不完全検査を変更", r, err)
	}
	if _, err = a.deleteExact(testContext(t), l.record.binding); err == nil {
		t.Fatal("完全検査なしの削除")
	}
	wrong := l.record.binding
	wrong.sid = wrong.machine + "-3000"
	if _, err = a.disable(testContext(t), wrong); err == nil {
		t.Fatal("別SID受理")
	}
	if calls != 1 {
		t.Fatal("拒否後dispatch", calls)
	}
}

func TestSIDSourceCloseRetainsInFlightOwnerAndRejectsLateSuccess(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	a, err := newSIDSource(l.plan, l.record.binding, f.s.accountSeal, f, func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) { close(entered); <-release; return sidResult(in), nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx := testContext(t)
	go func() { _, err := a.disable(ctx, l.record.binding); done <- err }()
	<-entered
	if err := a.Close(testContext(t)); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrUnknown) {
		t.Fatal("停止後の遅延成功", err)
	}
	if err := a.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if len(a.owners) != 1 {
		t.Fatal("worker完了前のowner喪失")
	}
	if _, err := a.disable(testContext(t), l.record.binding); err == nil {
		t.Fatal("close後に再発行")
	}
}

func TestSIDSourceCloseSignalsOwnerWhileMutationIsRunning(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	a, err := newSIDSource(l.plan, l.record.binding, f.s.accountSeal, f, func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) { close(entered); <-release; return nil, ErrUnknown }, close: func(context.Context) error {
			select {
			case stopped <- struct{}{}:
			default:
			}
			return ErrCleanup
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx := testContext(t)
	go func() { _, err := a.disable(ctx, l.record.binding); done <- err }()
	<-entered
	closeErr := a.Close(testContext(t))
	select {
	case <-stopped:
	default:
		t.Error("実行中ownerへの停止要求を省略")
	}
	if closeErr == nil {
		t.Error("mutator実return前に回収成功")
	}
	close(release)
	<-done
}
