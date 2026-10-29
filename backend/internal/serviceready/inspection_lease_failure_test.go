package serviceready

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestInspectionLeaseRejectsChangedRightsSecurityProcessAndSubject(t *testing.T) {
	for _, kind := range []string{"read-data", "inherit", "server", "current", "creation", "ACL", "token", "dirty-service", "restricted", "query-error"} {
		t.Run(kind, func(t *testing.T) {
			e, k := inspectionFixture(t)
			l, err := e.NewInspectionLease(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			h := &k.inspectionPipe.inspection
			switch kind {
			case "read-data":
				h.state.access |= 1
			case "inherit":
				h.state.flags = 1
			case "server":
				h.state.serverPID++
			case "current":
				h.state.currentPID++
			case "creation":
				h.state.creation++
			case "ACL":
				h.state.security.ACL = nil
			case "token":
				h.state.subject.modifiedID++
			case "dirty-service":
				h.state.subject.serviceAttributes = 0x14
			case "restricted":
				h.state.subject.restricted = true
			case "query-error":
				h.inspectErr = ErrDenied
			}
			if raw, err := l.Borrow(testContext(t)); raw != 0 || err == nil {
				t.Fatal("drift produced borrowed handle", raw, err)
			}
			if !e.failed.Load() || !e.query.revoked.Load() || e.inspectionActive.Load() != 1 {
				t.Fatal("failure lost admission/ownership")
			}
		})
	}
}
func TestInspectionLeasePartialDuplicateAndCloseFailureStayOwned(t *testing.T) {
	e, k := inspectionFixture(t)
	k.pipe.duplicateErr = ErrDenied
	l, err := e.NewInspectionLease(testContext(t))
	if l == nil || err == nil || l.ref == nil || e.inspection != l || e.inspectionActive.Load() != 1 {
		t.Fatal("partial duplicate lost", err)
	}
	h := &k.inspectionPipe.inspection
	h.closeErr = ErrUnconfirmed
	if err = l.Close(testContext(t)); !errors.Is(err, ErrUnconfirmed) || e.inspectionActive.Load() != 1 || h.handle == 0 {
		t.Fatal("failed close acknowledged", err)
	}
	if receipt, err := e.CloseEndpoint(testContext(t)); receipt != nil || !errors.Is(err, ErrUnconfirmed) || k.scope.closes != 0 || k.pipe.closes != 0 {
		t.Fatal("parent closed active duplicate", err)
	}
	h.closeErr = nil
	if err = l.Close(testContext(t)); err != nil || e.inspectionActive.Load() != 0 {
		t.Fatal(err)
	}
	if err = l.Close(testContext(t)); err != nil || h.closes != 2 {
		t.Fatal("close retried already closed reference", err)
	}
	if _, err = e.CloseEndpoint(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestInspectionLeasePanicBeforeDuplicateReturnRetainsReservedSlot(t *testing.T) {
	e, k := inspectionFixture(t)
	k.pipe.onDuplicate = func() { panic("native duplicate unreturned") }
	l, err := e.NewInspectionLease(testContext(t))
	if l == nil || !errors.Is(err, ErrFatal) || e.inspection != l || !l.unknown || e.inspectionActive.Load() != 1 {
		t.Fatal("panic lost reserved owner", err)
	}
	if err = l.Close(testContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unknown duplicate acknowledged", err)
	}
	if _, err = e.CloseEndpoint(testContext(t)); !errors.Is(err, ErrUnconfirmed) || k.scope.closes != 0 || k.pipe.closes != 0 {
		t.Fatal("unknown duplicate lost original resources", err)
	}
}
func TestInspectionLeaseCloseWaitsForActualReadOnlyQueryReturn(t *testing.T) {
	e, k := inspectionFixture(t)
	l, err := e.NewInspectionLease(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	k.inspectionPipe.inspection.onInspect = func() { once.Do(func() { close(entered) }); <-release }
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Recheck(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("query did not enter")
	}
	cancel()
	if err = l.Close(testContext(t)); !errors.Is(err, ErrUnconfirmed) || e.inspectionActive.Load() != 1 {
		close(release)
		t.Fatal("cancel released unreturned query", err)
	}
	close(release)
	if err = <-done; err == nil {
		t.Fatal("canceled query accepted")
	}
	if err = l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestInspectionLeaseCopyAndLateIssuanceCannotDuplicateReader(t *testing.T) {
	e, k := inspectionFixture(t)
	l, err := e.NewInspectionLease(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	var copied InspectionLease
	reflect.ValueOf(&copied).Elem().Set(reflect.ValueOf(l).Elem())
	if raw, err := copied.Borrow(testContext(t)); raw != 0 || err == nil {
		t.Fatal("copied lease borrowed")
	}
	if err = copied.Close(testContext(t)); err == nil || e.inspectionActive.Load() != 1 {
		t.Fatal("copied lease closed original")
	}
	if err = l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	before := len(k.pipe.duplicates)
	if another, err := e.NewInspectionLease(testContext(t)); another != nil || err == nil || len(k.pipe.duplicates) != before {
		t.Fatal("issuance slot reused")
	}
}
func TestInspectionLeasePostCloseActorFailureCannotAcknowledge(t *testing.T) {
	e, k := inspectionFixture(t)
	l, err := e.NewInspectionLease(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	h := &k.inspectionPipe.inspection
	h.onClose = func() { h.subjectErr = ErrFatal }
	if err = l.Close(testContext(t)); !errors.Is(err, ErrFatal) || e.inspectionActive.Load() != 1 {
		t.Fatal("post-close actor failure acknowledged", err)
	}
	h.onClose = nil
	h.subjectErr = nil
	queries, closes := h.subjectCalls, h.closes
	if err = l.Close(testContext(t)); !errors.Is(err, ErrFatal) || !errors.Is(err, ErrUnconfirmed) || e.inspectionActive.Load() != 1 || h.subjectCalls != queries || h.closes != closes || l.ref != h {
		t.Fatal("restored actor resumed unknown close", err)
	}
}

func TestInspectionLeaseFatalQueryCannotResumeAfterActorRestoration(t *testing.T) {
	e, k := inspectionFixture(t)
	l, err := e.NewInspectionLease(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	h := &k.inspectionPipe.inspection
	h.inspectErr = ErrFatal
	if err = l.Recheck(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	h.inspectErr = nil
	queries, subjects, closes := h.inspectCalls, h.subjectCalls, h.closes
	if err = l.Close(testContext(t)); !errors.Is(err, ErrFatal) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("fatal query resumed close", err)
	}
	if _, err = l.Borrow(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal("fatal query resumed borrow", err)
	}
	if h.inspectCalls != queries || h.subjectCalls != subjects || h.closes != closes || e.inspectionActive.Load() != 1 || l.ref != h {
		t.Fatal("unknown resources released or queried")
	}
}

func TestInspectionNativeCompletionCancellationStillChecksRawSubject(t *testing.T) {
	var c inspectionNativeCompletion
	rawCalls, releases := 0, 0
	dirty := true
	gate := func() error {
		rawCalls++
		if dirty {
			return ErrFatal
		}
		return nil
	}
	release := func() { releases++ }
	if err := c.complete(ErrDenied, gate, release); !errors.Is(err, ErrFatal) || releases != 0 || rawCalls != 1 {
		t.Fatal("cancel hid dirty subject or released pins", err)
	}
	dirty = false
	if err := c.complete(nil, gate, release); !errors.Is(err, ErrFatal) || releases != 0 || rawCalls != 1 {
		t.Fatal("restored actor resumed native completion", err)
	}
}

func TestInspectionNativeCompletionHealthyCancellationReleasesAfterRawGate(t *testing.T) {
	var c inspectionNativeCompletion
	checked, released := false, false
	err := c.complete(ErrDenied, func() error { checked = true; return nil }, func() {
		if !checked {
			t.Fatal("released before raw gate")
		}
		released = true
	})
	if !errors.Is(err, ErrDenied) || !released || c.fatal {
		t.Fatal("healthy canceled operation not settled", err)
	}
}

func TestInspectionNativeCompletionFatalNeverStartsTerminalNativeCall(t *testing.T) {
	var c inspectionNativeCompletion
	err := c.complete(errors.Join(ErrDenied, ErrFatal), func() error { t.Fatal("native resumed after fatal"); return nil }, func() { t.Fatal("fatal work released") })
	if !errors.Is(err, ErrFatal) || !c.fatal {
		t.Fatal(err)
	}
}

func TestInspectionLeaseFatalIssuanceRetainsDuplicateAndSlot(t *testing.T) {
	e, k := inspectionFixture(t)
	h := &k.inspectionPipe.inspection
	h.inspectErr = ErrFatal
	l, err := e.NewInspectionLease(testContext(t))
	if l == nil || !errors.Is(err, ErrFatal) || l.ref != h || !l.unknown {
		t.Fatal("fatal issuance lost owner", err)
	}
	h.inspectErr = nil
	queries, subjects, closes := h.inspectCalls, h.subjectCalls, h.closes
	if err = l.Close(testContext(t)); !errors.Is(err, ErrFatal) || e.inspectionActive.Load() != 1 || h.inspectCalls != queries || h.subjectCalls != subjects || h.closes != closes {
		t.Fatal("fatal issuance resumed", err)
	}
}
