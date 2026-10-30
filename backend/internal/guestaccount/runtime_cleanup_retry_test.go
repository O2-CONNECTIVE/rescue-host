package guestaccount

import "testing"

func TestRuntimeGuestAccountSourceClosePreservesOuterCleanupRetry(t *testing.T) {
	for _, name := range []string{"account-source", "journal", "held-ref", "scope"} {
		t.Run(name, func(t *testing.T) {
			p, c, f, k, ops := stagingTest(t)
			stage, err := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
			if err != nil {
				t.Fatal(err)
			}
			if err = stage.Prepare(testContext(t)); err != nil {
				t.Fatal(err)
			}
			r := &RuntimeGuest{stage: stage, created: stage.lifecycle.record.binding}
			r.self = r
			r.ops.scope = r.nativeScope
			extra := &stagingClosingFixture{}
			switch name {
			case "account-source":
				extra.err = ErrCleanup
				stage.accounts.owners = append(stage.accounts.owners, extra)
			case "journal":
				k.journal.closeErr = ErrCleanup
			case "held-ref":
				k.refs[len(k.refs)-1].err = ErrCleanup
			case "scope":
				k.scope.closeErr = ErrCleanup
			}
			if err = r.Close(testContext(t)); err == nil || r.closed || !stage.accountsClosing {
				t.Fatal("partial outer cleanup acknowledged", err)
			}
			extra.err = nil
			k.journal.closeErr = nil
			for _, held := range k.refs {
				held.err = nil
			}
			k.scope.closeErr = nil
			if err = r.Close(testContext(t)); err != nil || !r.closed || !stage.closed || stage.blocked {
				t.Fatal("normal account-source stop prevented original outer cleanup", err)
			}
		})
	}
}
