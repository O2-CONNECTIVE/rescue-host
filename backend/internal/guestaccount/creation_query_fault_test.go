package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type creationQueryKernelFixture struct {
	*accountQueryFixture
	roleCalls, domainCalls int
	roleHook, domainHook   func(int)
	entered, release       chan struct{}
	inflight               accountQueryMemory
	panicRole              bool
}

func (k *creationQueryKernelFixture) localRole() (accountQueryMemory, error) {
	k.roleCalls++
	if k.roleHook != nil {
		k.roleHook(k.roleCalls)
	}
	m, e := k.accountQueryFixture.localRole()
	k.inflight = m
	if k.entered != nil {
		close(k.entered)
		<-k.release
	}
	if k.panicRole {
		panic("synthetic role API return uncertainty")
	}
	return m, e
}
func (k *creationQueryKernelFixture) localDomain() (accountQueryMemory, error) {
	k.domainCalls++
	if k.domainHook != nil {
		k.domainHook(k.domainCalls)
	}
	return k.accountQueryFixture.localDomain()
}

func TestCreationQueryRejectsDCMachineDriftAndUnknownParent(t *testing.T) {
	for _, kind := range []string{"dc", "foreign-machine", "role-drift", "machine-drift", "missing-role", "partial-role", "subject", "free-scope-loss"} {
		t.Run(kind, func(t *testing.T) {
			q, f, _ := accountQueryTest(t)
			k := &creationQueryKernelFixture{accountQueryFixture: f}
			switch kind {
			case "dc":
				f.role.value.role = 4
			case "foreign-machine":
				f.domain.value.sid = "S-1-5-21-40-50-60"
			case "role-drift":
				k.roleHook = func(n int) {
					if n == 2 {
						f.role.value.role = 2
					}
				}
			case "machine-drift":
				k.domainHook = func(n int) {
					if n == 2 {
						f.domain.value.sid = "S-1-5-21-40-50-60"
					}
				}
			case "missing-role":
				f.role = nil
			case "partial-role":
				f.roleErr = ErrUnknown
			case "subject":
				f.principalValue.threadToken = true
			case "free-scope-loss":
				f.releaseHook = func(string) { f.principalValue.modifiedID++ }
			}
			o, e := newCreationCheck(q.plan, q.seal, k)
			if e != nil {
				t.Fatal(e)
			}
			if e = o.check(testContext(t)); e == nil {
				t.Fatal("unconfirmed parent accepted")
			}
			for _, step := range f.trace {
				if strings.HasPrefix(step, "user:") {
					t.Fatal("preflight adopted name lookup")
				}
			}
		})
	}
}

func TestCreationQueryFatalAndPendingResourcesRemainUnknownWithoutRetry(t *testing.T) {
	for _, kind := range []string{"scope-fatal", "free-failure", "source-panic"} {
		t.Run(kind, func(t *testing.T) {
			q, f, b := accountQueryTest(t)
			k := &creationQueryKernelFixture{accountQueryFixture: f}
			var o *creationQueryOwner
			var e error
			switch kind {
			case "scope-fatal":
				f.userHook = func() { f.principalValue.modifiedID++ }
				o, e = newCreationInspector(q.plan, b, q.seal, k)
			case "free-failure":
				f.user.releaseErr = ErrCleanup
				o, e = newCreationInspector(q.plan, b, q.seal, k)
			case "source-panic":
				k.panicRole = true
				o, e = newCreationCheck(q.plan, q.seal, k)
			}
			if e != nil {
				t.Fatal(e)
			}
			if o.creation {
				e = o.check(testContext(t))
			} else {
				_, e = o.inspect(testContext(t), b)
			}
			if !errors.Is(e, ErrUnknown) {
				t.Fatal("unknown native result not retained", e)
			}
			before := append([]string(nil), f.trace...)
			calls := f.principalCalls
			if e = o.Close(testContext(t)); !errors.Is(e, ErrUnknown) {
				t.Fatal("unknown resources acknowledged", e)
			}
			if o.creation {
				e = o.check(testContext(t))
			} else {
				_, e = o.inspect(testContext(t), b)
			}
			if !errors.Is(e, ErrUnknown) || !reflect.DeepEqual(before, f.trace) || f.principalCalls != calls {
				t.Fatal("fatal/pending owner restarted native calls", e)
			}
			if kind == "source-panic" && k.inflight == nil {
				t.Fatal("source out reference lost")
			}
		})
	}
}

func TestCreationQueryCanceledCallerRetainsLateNativeReturnAndBlocksCloseAck(t *testing.T) {
	q, f, _ := accountQueryTest(t)
	k := &creationQueryKernelFixture{accountQueryFixture: f, entered: make(chan struct{}), release: make(chan struct{})}
	o, e := newCreationCheck(q.plan, q.seal, k)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- o.check(ctx) }()
	<-k.entered
	cancel()
	select {
	case e = <-result:
		if !errors.Is(e, ErrUnknown) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		close(k.release)
		t.Fatal("caller did not return at cancellation")
	}
	cleanup, end := context.WithTimeout(context.Background(), 10*time.Millisecond)
	e = o.Close(cleanup)
	end()
	if !errors.Is(e, ErrUnknown) {
		close(k.release)
		t.Fatal("source nonreturn was acknowledged", e)
	}
	if o.query.kernel != k || k.inflight == nil {
		close(k.release)
		t.Fatal("held kernel/out reference lost")
	}
	for _, step := range f.trace {
		if strings.HasPrefix(step, "free:") {
			close(k.release)
			t.Fatal("native resource freed before actual return")
		}
	}
	close(k.release)
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal("healthy actual cleanup could not be acknowledged", e)
	}
	before := append([]string(nil), f.trace...)
	if e = o.check(testContext(t)); !errors.Is(e, ErrUnknown) || !reflect.DeepEqual(before, f.trace) {
		t.Fatal("cancelled owner reused")
	}
}

func TestCreationQueryInspectorSerializesWithoutDiscardingHealthyCurrentCall(t *testing.T) {
	q, f, b := accountQueryTest(t)
	f.entered = make(chan struct{})
	f.unblock = make(chan struct{})
	o, e := newCreationInspector(q.plan, b, q.seal, f)
	if e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := o.inspect(testContext(t), b); result <- e }()
	<-f.entered
	if _, e = o.inspect(testContext(t), b); !errors.Is(e, ErrBusy) {
		close(f.unblock)
		t.Fatal("overlapping query admitted", e)
	}
	close(f.unblock)
	if e = <-result; e != ErrUnconfigured {
		t.Fatal(e)
	}
	f.entered = nil
	if s, e := o.inspect(testContext(t), b); e != ErrUnconfigured || s.complete || !s.disabled {
		t.Fatal("healthy retry changed diagnostic contract", s, e)
	}
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestCreationQueryCloseStopsLaterNativePhasesWithoutCancelingCallerContext(t *testing.T) {
	q, f, _ := accountQueryTest(t)
	k := &creationQueryKernelFixture{accountQueryFixture: f, entered: make(chan struct{}), release: make(chan struct{})}
	o, e := newCreationCheck(q.plan, q.seal, k)
	if e != nil {
		t.Fatal(e)
	}
	ctx := testContext(t)
	result := make(chan error, 1)
	go func() { result <- o.check(ctx) }()
	<-k.entered
	cleanup, end := context.WithTimeout(context.Background(), 10*time.Millisecond)
	e = o.Close(cleanup)
	end()
	if !errors.Is(e, ErrUnknown) {
		close(k.release)
		t.Fatal("unreturned role acknowledged", e)
	}
	close(k.release)
	if e = <-result; !errors.Is(e, ErrUnknown) {
		t.Fatal(e)
	}
	if ctx.Err() != nil {
		t.Fatal("Close changed caller context")
	}
	for _, step := range f.trace {
		if step == "domain" {
			t.Fatal("Close let a later native phase start", f.trace)
		}
	}
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestCreationQueryHealthyNativeRejectionDoesNotBecomeScopeUnknown(t *testing.T) {
	for _, kind := range []string{"dc", "domain", "sid"} {
		q, f, b := accountQueryTest(t)
		switch kind {
		case "dc":
			f.role.value.role = 4
		case "domain":
			f.domain.value.sid = "S-1-5-21-40-50-60"
		case "sid":
			f.user.value.sid = b.machine + "-9999"
		}
		o, e := newCreationInspector(q.plan, b, q.seal, f)
		if e != nil {
			t.Fatal(e)
		}
		if s, e := o.inspect(testContext(t), b); !errors.Is(e, ErrDenied) || s.seal != nil || s.complete {
			t.Fatalf("healthy native %s rejection changed scope classification: %+v %v", kind, s, e)
		}
		if e = o.Close(testContext(t)); e != nil {
			t.Fatal("healthy rejected query retained unknown resources", e)
		}
	}
}
