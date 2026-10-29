package guestaccount

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type creationCheckFixture struct{ f *fixture }

func (c creationCheckFixture) check(context.Context) error { return c.f.step("create-preflight") }
func (c creationCheckFixture) Close(context.Context) error { return nil }

type creationInspectorFixture struct{ *fixture }

func (c creationInspectorFixture) Close(context.Context) error { return nil }

func TestCreationSourceRunsFromJournalAttemptThroughOwnedSIDAndCleanup(t *testing.T) {
	l, f := newFixture(t)
	p := l.plan
	var made binding
	deps := creationDependencies{preflight: creationCheckFixture{f}, create: func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) {
			if !bytes.Equal(in, creationRequest(p.binding)) {
				t.Fatal("変更された作成要求")
			}
			if f.stored.stage != createAttempt {
				t.Fatal("作成意図を先に保存していない")
			}
			f.step("create-native")
			made = p.binding
			made.sid = made.machine + "-2002"
			f.snapshot = accountSnapshot{seal: f.s.accountSeal, binding: made, presence: exact, complete: true, disabled: true, groups: []string{"synthetic-users"}, privileges: []string{}, logonRights: []string{"synthetic-ssh"}}
			return creationResult(in, made.sid, true, true), nil
		}}, nil
	}, inspect: func(b binding) (ownedAccountInspector, error) {
		if b != made {
			t.Fatal("別SIDに検査を接続")
		}
		return creationInspectorFixture{f}, nil
	}, cleanup: func() (sidExecutor, error) {
		return &sidTestExecutor{run: func(in []byte) ([]byte, error) {
			if !strings.Contains(string(in), `"sid":"S-1-5-21-10-20-30-2002"`) {
				t.Fatal("別SIDの清掃")
			}
			return sidResult(in), nil
		}}, nil
	}}
	a, err := newCreationSource(p, f.s.accountSeal, deps)
	if err != nil {
		t.Fatal(err)
	}
	l.sources.accounts = a
	if err = l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if l.record.binding != made || l.record.stage != preparedDisabled {
		t.Fatal("所有SIDをcoreへ保存しない")
	}
	if err = l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if f.stored.stage != deleteComplete {
		t.Fatal("終端未確認")
	}
}
