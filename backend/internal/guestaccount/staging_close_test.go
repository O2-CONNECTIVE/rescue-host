package guestaccount

import (
	"context"
	"testing"
)

type stagingClosingFixture struct {
	err    error
	closes int
}

func (f *stagingClosingFixture) Close(context.Context) error { f.closes++; return f.err }
func TestStagingAccountClosingCanRetryRemainingOuterCleanup(t *testing.T) {
	for _, name := range []string{"account-source", "journal", "held-ref", "scope"} {
		t.Run(name, func(t *testing.T) {
			p, c, f, k, ops := stagingTest(t)
			o, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
			if e != nil {
				t.Fatal(e)
			}
			if e = o.Prepare(testContext(t)); e != nil {
				t.Fatal(e)
			}
			pre, ok := o.accounts.deps.preflight.(*creationQueryOwner)
			if !ok {
				t.Fatal("native型preflightを検査しないfixture")
			}
			extra := &stagingClosingFixture{}
			switch name {
			case "account-source":
				extra.err = ErrCleanup
				o.accounts.owners = append(o.accounts.owners, extra)
			case "journal":
				k.journal.closeErr = ErrCleanup
			case "held-ref":
				k.refs[len(k.refs)-1].err = ErrCleanup
			case "scope":
				k.scope.closeErr = ErrCleanup
			}
			if e = o.Close(testContext(t)); e == nil || o.closed || !pre.stop.Load() {
				t.Fatal("sourceClose後の未確認をack", e)
			}
			extra.err = nil
			k.journal.closeErr = nil
			for _, r := range k.refs {
				r.err = nil
			}
			k.scope.closeErr = nil
			if e = o.Close(testContext(t)); e != nil || !o.closed || o.blocked {
				t.Fatal("正常なsource stopから回収不能", e)
			}
		})
	}
}
