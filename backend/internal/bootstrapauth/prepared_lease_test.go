package bootstrapauth

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func preparedLeaseFixture(t *testing.T) (*PreparedService, *preparingKernel, Profile) {
	t.Helper()
	f, a := preparingFixture(t)
	p, e := f.PrepareService(authContext(t), 73)
	if e != nil {
		t.Fatal(e)
	}
	return p, a, testProfile()
}
func TestPreparedLeaseHoldsParentAdmissionAndReturnsOnlyLogicalGate(t *testing.T) {
	p, a, profile := preparedLeaseFixture(t)
	l, e := p.HoldProfile(authContext(t), profile)
	if e != nil || l == nil {
		t.Fatal("exact prepared profile was not held", e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if e = p.Close(ctx); e != ErrUnconfirmed {
		t.Fatal("parent closed while lease held", e)
	}
	if e = p.Recheck(ctx); e != ErrDenied {
		t.Fatal("parent bypassed lease", e)
	}
	if e = l.Recheck(authContext(t)); e != nil {
		t.Fatal(e)
	}
	for _, file := range a.files {
		if file.closes != 0 {
			t.Fatal("lease closed original references")
		}
	}
	if e = l.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if e = l.Close(authContext(t)); e != nil {
		t.Fatal("healthy close not idempotent", e)
	}
	if e = l.Recheck(authContext(t)); e != ErrClosed {
		t.Fatal("released lease reused", e)
	}
	if e = p.Recheck(authContext(t)); e != nil {
		t.Fatal("logical release invalidated parent", e)
	}
	if e = p.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	for _, file := range a.files {
		if file.closes != 1 {
			t.Fatal("parent lost original references")
		}
	}
	if a.forbiddenQueries != 0 {
		t.Fatal("lease used SCM/peer/token mutation")
	}
}
func TestPreparedLeaseRejectsDifferentProfileAndCopiesExpected(t *testing.T) {
	p, a, profile := preparedLeaseFixture(t)
	profile.Keys.GuestSID = "S-1-5-21-1-2-3-1900"
	l, e := p.HoldProfile(authContext(t), profile)
	if e != ErrConfig || l == nil {
		t.Fatal("profile mismatch adopted or lost gate owner", e)
	}
	if e = l.Close(authContext(t)); e != nil {
		t.Fatal("healthy rejected lease could not release", e)
	}
	if e = p.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	for _, file := range a.files {
		if file.closes != 1 {
			t.Fatal("mismatch lost originals")
		}
	}
	p, _, profile = preparedLeaseFixture(t)
	l, e = p.HoldProfile(authContext(t), profile)
	if e != nil {
		t.Fatal(e)
	}
	profile.SupervisorImage.Nodes[0].ACL[0].Mask = 0
	if e = l.Recheck(authContext(t)); e != nil {
		t.Fatal("caller mutation changed retained profile", e)
	}
	if e = l.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if e = p.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}
func TestPreparedLeaseCancellationCannotReleaseBeforeVerificationReturns(t *testing.T) {
	p, a, profile := preparedLeaseFixture(t)
	l, e := p.HoldProfile(authContext(t), profile)
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.files[len(a.files)-1].onRead = func() { once.Do(func() { close(entered) }); <-release }
	ctx, cancel := context.WithCancel(authContext(t))
	done := make(chan error, 1)
	go func() { done <- l.Recheck(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("image verification not entered")
	}
	cancel()
	if e = l.Close(authContext(t)); e != ErrUnconfirmed {
		close(release)
		t.Fatal("inflight verification gate released", e)
	}
	parentCtx, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer stop()
	if e = p.Close(parentCtx); e != ErrUnconfirmed {
		close(release)
		t.Fatal("parent cleanup ran before return", e)
	}
	close(release)
	if e = <-done; e != ErrDenied {
		t.Fatal("canceled verification succeeded", e)
	}
	if e = l.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if e = p.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}
func TestPreparedLeaseFatalAndPanicRetainOriginalScopeAndGate(t *testing.T) {
	for _, kind := range []string{"foreign-thread", "dirty", "panic"} {
		t.Run(kind, func(t *testing.T) {
			p, a, profile := preparedLeaseFixture(t)
			switch kind {
			case "foreign-thread":
				a.id++
			case "dirty":
				a.dirty = true
			case "panic":
				a.onCurrentImage = func() { panic("native source panic") }
			}
			l, e := p.HoldProfile(authContext(t), profile)
			if l == nil || !errors.Is(e, ErrFatal) {
				t.Fatal("unknown hold lost owner/fatal", e)
			}
			before := a.queryCalls
			if e = l.Close(authContext(t)); !errors.Is(e, ErrFatal) {
				t.Fatal("fatal lease returned gate", e)
			}
			if a.queryCalls != before {
				t.Fatal("poisoned lease queried again")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			if e = p.Close(ctx); e != ErrUnconfirmed {
				t.Fatal("parent bypassed unknown lease", e)
			}
			for _, file := range a.files {
				if file.closes != 0 {
					t.Fatal("fatal lease discarded references")
				}
			}
		})
	}
}

func TestPreparedLeaseRejectsCopyZeroAndCancelledCloseWithoutReturningGate(t *testing.T) {
	p, a, profile := preparedLeaseFixture(t)
	l, e := p.HoldProfile(authContext(t), profile)
	if e != nil {
		t.Fatal(e)
	}
	var copied PreparedLease
	// 実際のvalue copyを再現し、mutexを含む型の通常copyを推奨しない。
	reflect.ValueOf(&copied).Elem().Set(reflect.ValueOf(l).Elem())
	before := a.queryCalls
	if copied.Recheck(authContext(t)) != ErrClosed || copied.Close(authContext(t)) != ErrClosed {
		t.Fatal("copied lease used original gate")
	}
	var zero PreparedLease
	if zero.Recheck(authContext(t)) != ErrClosed || zero.Close(authContext(t)) != ErrClosed {
		t.Fatal("zero lease was accepted")
	}
	ctx, cancel := context.WithCancel(authContext(t))
	cancel()
	if e = l.Close(ctx); e != ErrUnconfirmed {
		t.Fatal("cancelled close returned gate", e)
	}
	if a.queryCalls != before || len(p.gate) != 1 {
		t.Fatal("invalid close queried or returned admission")
	}
	if e = l.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if e = p.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}
func TestPreparedLeaseRecheckRejectsImageDriftWithoutRevivingParent(t *testing.T) {
	p, a, profile := preparedLeaseFixture(t)
	l, e := p.HoldProfile(authContext(t), profile)
	if e != nil {
		t.Fatal(e)
	}
	a.files[1].value.id = [16]byte{7}
	if e = l.Recheck(authContext(t)); e != ErrDenied {
		t.Fatal("same lease accepted changed image", e)
	}
	a.files[1].value.id = [16]byte{1}
	if e = l.Recheck(authContext(t)); e != ErrDenied {
		t.Fatal("failed lease revived", e)
	}
	if e = l.Close(authContext(t)); e != nil {
		t.Fatal("healthy failed scope did not release gate", e)
	}
	if e = p.Recheck(authContext(t)); e != ErrDenied {
		t.Fatal("parent prepared state revived", e)
	}
	if e = p.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}
func TestPreparedLeaseHoldCancellationRetainsOwnerUntilActualReturn(t *testing.T) {
	p, a, profile := preparedLeaseFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.files[1].onRead = func() { once.Do(func() { close(entered) }); <-release }
	ctx, cancel := context.WithCancel(authContext(t))
	defer cancel()
	type outcome struct {
		lease *PreparedLease
		err   error
	}
	done := make(chan outcome, 1)
	go func() { l, e := p.HoldProfile(ctx, profile); done <- outcome{l, e} }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hold verify not entered")
	}
	cancel()
	parentCtx, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer stop()
	if e := p.Close(parentCtx); e != ErrUnconfirmed {
		close(release)
		t.Fatal("hold cancellation released original references", e)
	}
	close(release)
	out := <-done
	if out.lease == nil || out.err != ErrDenied {
		t.Fatal("partial cancelled hold owner lost", out.err)
	}
	if e := out.lease.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if e := p.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}
func TestPreparedLeaseCancellationCannotHideDirtyVerification(t *testing.T) {
	p, a, profile := preparedLeaseFixture(t)
	l, e := p.HoldProfile(authContext(t), profile)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(authContext(t))
	defer cancel()
	a.files[1].onRead = func() { a.dirty = true; cancel() }
	if e = l.Recheck(ctx); e != ErrFatal {
		t.Fatal("cancellation hid dirty subject", e)
	}
	if e = l.Close(authContext(t)); e != ErrFatal || len(p.gate) != 1 {
		t.Fatal("dirty lease released", e)
	}
}
