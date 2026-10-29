package guestaccount

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCreationQueryChecksParentWithoutLookingUpOrAdoptingName(t *testing.T) {
	q, f, _ := accountQueryTest(t)
	o, e := newCreationCheck(q.plan, q.seal, f)
	if e != nil {
		t.Fatal(e)
	}
	if len(f.trace) != 0 {
		t.Fatal("pure constructor queried native state")
	}
	if e = o.check(testContext(t)); e != nil {
		t.Fatal(e)
	}
	want := []string{"role", "decode:role", "domain", "decode:domain", "role", "decode:role", "domain", "decode:domain", "free:domain", "free:role", "free:domain", "free:role"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatal("creation mode skipped parent recheck or queried an existing user", f.trace)
	}
	if e = o.check(testContext(t)); e == nil {
		t.Fatal("creation preflight reused")
	}
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestCreationQueryInspectorKeepsIncompleteDiagnosisAndSerialHealthyRetry(t *testing.T) {
	q, f, b := accountQueryTest(t)
	o, e := newCreationInspector(q.plan, b, q.seal, f)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		s, e := o.inspect(testContext(t), b)
		if e != ErrUnconfigured || s.binding != b || s.seal != q.seal || !s.disabled || s.complete || s.presence != exact {
			t.Fatal("partial diagnosis became complete/absent or lost SID", s, e)
		}
	}
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	before := append([]string(nil), f.trace...)
	if _, e = o.inspect(testContext(t), b); !errors.Is(e, ErrUnknown) || !reflect.DeepEqual(before, f.trace) {
		t.Fatal("closed inspector queried again", e)
	}
}

func TestCreationQueryModeBindingAndConstructorsRefuseBeforeNativeCalls(t *testing.T) {
	q, f, b := accountQueryTest(t)
	p := q.plan
	invalid := p
	invalid.binding.sid = b.sid
	if o, e := newCreationCheck(invalid, q.seal, f); e == nil || o != nil {
		t.Fatal("SID-present creation Plan accepted")
	}
	if o, e := newCreationInspector(p, p.binding, q.seal, f); e == nil || o != nil {
		t.Fatal("SID-less inspector accepted")
	}
	check, e := newCreationCheck(p, q.seal, f)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = check.inspect(testContext(t), b); e == nil {
		t.Fatal("creation checker used as inspector")
	}
	inspector, e := newCreationInspector(p, b, q.seal, f)
	if e != nil {
		t.Fatal(e)
	}
	if e = inspector.check(testContext(t)); e == nil {
		t.Fatal("inspector used as creation preflight")
	}
	foreign := b
	foreign.intent[0]++
	if _, e = inspector.inspect(testContext(t), foreign); e == nil {
		t.Fatal("foreign binding accepted")
	}
	if e = check.check(context.Background()); e == nil {
		t.Fatal("unbounded preflight accepted")
	}
	copyOwner := creationQueryOwner{self: check, query: check.query, creation: true}
	if e = copyOwner.check(testContext(t)); e != ErrUnconfigured {
		t.Fatal("copied owner used", e)
	}
	if e = copyOwner.Close(testContext(t)); e != ErrUnconfigured {
		t.Fatal("copied owner closed original")
	}
	if len(f.trace) != 0 || f.principalCalls != 0 {
		t.Fatal("pure rejection queried OS", f.trace, f.principalCalls)
	}
}
