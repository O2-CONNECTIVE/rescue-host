package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHelperEntryNativeWithoutTrustedSourceStaysUnconfigured(t *testing.T) {
	for _, args := range [][]string{nil, {"--operator-auth-internal", "9", "10", "77", "untrusted"}} {
		if err := runNativeFixedHelper(context.Background(), args); !errors.Is(err, ErrUnconfigured) {
			t.Fatalf("unconfigured native entry=%v", err)
		}
	}
}

type entryLeaseFixture struct {
	cfg             helperEntryConfig
	checked, closed int
	checkErr        error
	closeErr        error
	closeCheck      func(context.Context)
	checkFn         func()
}

func (f *entryLeaseFixture) config() helperEntryConfig { return f.cfg }
func (f *entryLeaseFixture) check(context.Context, helperEntryConfig) error {
	f.checked++
	if f.checkFn != nil {
		f.checkFn()
	}
	return f.checkErr
}
func (f *entryLeaseFixture) receiverSource() profileReceiverSource {
	return profileReceiverCheck(func(context.Context, profileReceiverBinding) error { return nil })
}
func (f *entryLeaseFixture) close(ctx context.Context) error {
	f.closed++
	if f.closeCheck != nil {
		f.closeCheck(ctx)
	}
	return f.closeErr
}

type entrySourceFixture struct {
	lease    *entryLeaseFixture
	prepared int
}

func (f *entrySourceFixture) prepare(context.Context) (helperEntryLease, error) {
	f.prepared++
	return f.lease, nil
}

func entryFixture(t *testing.T) (*entrySourceFixture, helperEntryFactory, *reportFake, *testThread, *[]uintptr) {
	t.Helper()
	frame := receiverFrame()
	ctx, receiver, _ := receiverSetup(t, receiverRead{data: frame, n: uint32(len(frame))}, receiverRead{err: errProfileSyncEOF})
	_ = ctx
	lease := &entryLeaseFixture{cfg: helperEntryConfig{binding: receiver.binding, operatorRead: 9, reportWrite: 10, runBudget: time.Second, cleanup: 100 * time.Millisecond}}
	kernel := &reportFake{replies: []reportReply{{data: string(requestFrame()), n: uint32(len(requestFrame()))}, {n: uint32(len(requestFrame()))}}}
	_, thread, _, _ := helperFixture()
	profile, err := decodeHelperProfileFrame(frame, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	thread.projection = profile.operator
	raw := []uintptr{}
	factory := helperEntryFactory{
		message: func(h uintptr, role endpointRole, budget time.Duration) (*messageEndpoint, error) {
			if lease.checked == 0 {
				t.Error("native adoption before retained source check")
			}
			return newMessageEndpoint(h, role, budget, kernel)
		},
		profile: func(context.Context, uintptr, profileReceiverBinding, profileReceiverSource, time.Duration) (*profileReceiver, error) {
			return receiver, nil
		},
		thread:   func() threadKernel { return thread },
		closeRaw: func(h uintptr) error { raw = append(raw, h); return nil },
	}
	return &entrySourceFixture{lease: lease}, factory, kernel, thread, &raw
}
func entryArgs() []string {
	return []string{"--operator-auth-internal", "9", "10", "77", strings.Repeat("a", 64)}
}

func TestHelperEntryConsumesTrustedProfileThenAuthenticatesAndCloses(t *testing.T) {
	source, factory, kernel, thread, raw := entryFixture(t)
	if err := runFixedHelper(requestContext(t), entryArgs(), source, factory); err != nil {
		t.Fatal(err)
	}
	if source.prepared != 1 || source.lease.closed != 1 || source.lease.checked < 4 || len(*raw) != 0 || !reflect.DeepEqual(kernel.closed, []uintptr{10, 9}) {
		t.Fatalf("prepare=%d checks=%d close=%d endpoints=%v raw=%v", source.prepared, source.lease.checked, source.lease.closed, kernel.closed, *raw)
	}
	if thread.cleanCount != 2 {
		t.Fatal("message token was not restored", thread.cleanCount)
	}
}

func TestHelperEntryUnknownRawCloseCannotReportEvidenceReleased(t *testing.T) {
	source, factory, _, _, _ := entryFixture(t)
	source.lease.checkErr = ErrDenied
	factory.closeRaw = func(uintptr) error { panic("unknown close result") }
	if err := runFixedHelper(requestContext(t), entryArgs(), source, factory); !errors.Is(err, ErrUnconfirmed) || source.lease.closed != 0 {
		t.Fatalf("unknown cleanup=%v evidence closes=%d", err, source.lease.closed)
	}
}

func TestHelperEntryCleanupKeepsLeaseWhenRawOwnershipIsIncomplete(t *testing.T) {
	source, factory, _, _, _ := entryFixture(t)
	source.lease.cfg.binding.profileRead = 0
	if err := runFixedHelper(requestContext(t), entryArgs(), source, factory); !errors.Is(err, ErrUnconfirmed) || source.lease.closed != 0 {
		t.Fatalf("incomplete ownership=%v evidence closes=%d", err, source.lease.closed)
	}
}

func TestHelperEntryRejectsCancellationDuringCleanup(t *testing.T) {
	source, factory, _, _, _ := entryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	source.lease.closeCheck = func(context.Context) { cancel() }
	if err := runFixedHelper(ctx, entryArgs(), source, factory); err == nil {
		t.Fatal("cancelled entry returned success")
	}
}

func TestHelperEntryRejectsUnmatchedArgumentsBeforeAdoption(t *testing.T) {
	for _, args := range [][]string{nil, {"--operator-auth-internal", "09", "10", "77", strings.Repeat("a", 64)}, {"--operator-auth-internal", "9", "10", "77", strings.Repeat("b", 64)}} {
		source, factory, _, _, raw := entryFixture(t)
		factory.message = func(uintptr, endpointRole, time.Duration) (*messageEndpoint, error) {
			t.Fatal("unmatched argv adopted handle")
			return nil, ErrDenied
		}
		if err := runFixedHelper(requestContext(t), args, source, factory); err == nil {
			t.Fatal("unmatched arguments accepted")
		}
		if source.lease.closed != 1 || len(*raw) != 3 {
			t.Fatal("raw handles were not recovered", *raw)
		}
	}
}

func TestHelperEntryRetainsPartialAdoptionOwnership(t *testing.T) {
	source, factory, kernel, _, raw := entryFixture(t)
	adopt := factory.message
	factory.message = func(h uintptr, r endpointRole, b time.Duration) (*messageEndpoint, error) {
		p, e := adopt(h, r, b)
		if e != nil {
			return p, e
		}
		return p, ErrDenied
	}
	if err := runFixedHelper(requestContext(t), entryArgs(), source, factory); err == nil {
		t.Fatal("partial adoption accepted")
	}
	if !reflect.DeepEqual(kernel.closed, []uintptr{9}) || !reflect.DeepEqual(*raw, []uintptr{10, 77}) || source.lease.closed != 1 {
		t.Fatal("partial ownership lost", kernel.closed, *raw)
	}
}

func TestHelperEntryPanicMakesFixedThreadFatalBeforeCleanup(t *testing.T) {
	source, factory, kernel, _, raw := entryFixture(t)
	source.lease.checkFn = func() { panic("unknown fixed-thread subject") }
	done := make(chan error, 1)
	ctx := requestContext(t)
	go func() { done <- runFixedHelper(ctx, entryArgs(), source, factory) }()
	if err := <-done; !errors.Is(err, ErrFatal) {
		t.Fatal("thread panic returned reusable state", err)
	}
	if len(*raw) != 0 || len(kernel.closed) != 0 || source.lease.closed != 0 {
		t.Fatal("fatal thread performed cleanup")
	}
}

func TestHelperEntryPreservesFatalFromLeaseCleanup(t *testing.T) {
	source, factory, _, _, _ := entryFixture(t)
	source.lease.closeErr = ErrFatal
	done := make(chan error, 1)
	ctx := requestContext(t)
	go func() { done <- runFixedHelper(ctx, entryArgs(), source, factory) }()
	if err := <-done; !errors.Is(err, ErrFatal) {
		t.Fatal("cleanup fatal was lost", err)
	}
}

func TestHelperRetainsFatalFromEverySourceCheck(t *testing.T) {
	for _, failure := range []int{1, 2, 3} {
		h, _, events, published := helperFixture()
		checks := 0
		h.ops.check = func(context.Context) error {
			checks++
			if checks == failure {
				return ErrFatal
			}
			return nil
		}
		if err := h.Run(requestContext(t)); !errors.Is(err, ErrFatal) {
			t.Fatal("source fatal lost", failure, err)
		}
		if len(*published) != 0 || (*events)[len(*events)-1] != "abort" {
			t.Fatal("fatal source was reused", failure, *events)
		}
	}
}
