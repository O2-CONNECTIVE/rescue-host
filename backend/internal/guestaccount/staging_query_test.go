package guestaccount

import (
	"context"
	"testing"
)

func TestStagingPreflightUnknownCannotRestartOuterNative(t *testing.T) {
	for _, name := range []string{"fatal", "pending", "panic"} {
		t.Run(name, func(t *testing.T) {
			p, c, f, k, ops := stagingTest(t)
			accounts := ops.accounts
			_, qf, _ := accountQueryTest(t)
			query := &creationQueryKernelFixture{accountQueryFixture: qf}
			switch name {
			case "fatal":
				query.roleHook = func(int) { qf.principalValue.modifiedID++ }
			case "pending":
				qf.role.releaseErr = ErrCleanup
			case "panic":
				query.panicRole = true
			}
			ops.accounts = func(p Plan, s *seal, profile permissionProfile) (*creationSource, error) {
				a, e := accounts(p, s, profile)
				if e != nil {
					return a, e
				}
				pre, e := newCreationCheck(p, s, query)
				a.deps.preflight = pre
				return a, e
			}
			o, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
			if e != nil {
				t.Fatal(e)
			}
			checks := k.scope.checks
			if e = o.Prepare(testContext(t)); e == nil {
				t.Fatal("unsafe preflightを受理")
			}
			if k.scope.checks != checks+1 {
				t.Fatal("preflight unknown後のnative再照会")
			}
			calls := k.principalCalls
			checks = k.scope.checks
			_ = o.Prepare(testContext(t))
			_ = o.Close(testContext(t))
			if k.principalCalls != calls || k.scope.checks != checks || k.scope.closes != 0 || k.journal.closes != 0 {
				t.Fatal("unsafe preflight後の取得/解放")
			}
		})
	}
}
func TestStagingPreflightNonreturnKeepsBorrowedObjectsAfterCancel(t *testing.T) {
	p, c, f, k, ops := stagingTest(t)
	accounts := ops.accounts
	_, qf, _ := accountQueryTest(t)
	query := &creationQueryKernelFixture{accountQueryFixture: qf, entered: make(chan struct{}), release: make(chan struct{})}
	var pre *creationQueryOwner
	ops.accounts = func(p Plan, s *seal, profile permissionProfile) (*creationSource, error) {
		a, e := accounts(p, s, profile)
		if e != nil {
			return a, e
		}
		pre, e = newCreationCheck(p, s, query)
		a.deps.preflight = pre
		return a, e
	}
	o, e := newStaging(testContext(t), p, c, f, f.s.sessionSeal, ops)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	done := make(chan error, 1)
	checks := k.scope.checks
	go func() { done <- o.Prepare(ctx) }()
	<-query.entered
	cancel()
	if e = <-done; e == nil || k.scope.checks != checks+1 {
		close(query.release)
		t.Fatal("実return前の借用解放/再照会", e)
	}
	calls := k.principalCalls
	_ = o.Close(testContext(t))
	if k.principalCalls != calls || k.scope.closes != 0 || k.journal.closes != 0 {
		close(query.release)
		t.Fatal("非return資源を回収ack")
	}
	close(query.release)
	<-pre.work.done
}
