package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func TestSoleInputBatchMovesOnlyAfterPreparedOwnerRegistration(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	s.input = 0
	s.soleInput = new(serviceready.SoleInput)
	kernel := endpointCandidateFixture()
	s.inputLease = &endpointCandidateLease{api: kernel}
	registered, moves := false, 0
	ops.moveSoleInput = func(_ context.Context, input *serviceready.SoleInput) (uintptr, error) {
		if !registered || input != s.soleInput {
			t.Error("move preceded exact prepared owner registration")
		}
		moves++
		*events = append(*events, "move-input")
		return 31, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if moves != 0 || kernel.opened != 0 || b.input != 0 || b.Collector() == nil || !b.MatchesOwner(owner.owner, owner.issuer) {
		t.Fatal("prepare acquired input or replaced registered owner")
	}
	registered = true
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if moves != 1 || (*events)[0] != "move-input" {
		t.Fatal("registered Start did not move before I/O", moves, *events)
	}
	if err := b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func soleInputSetup(t *testing.T) (nativeBatchSpec, batchOwner, nativeBatchOps, *[]string, *memoryBatchChild, *soleInputCandidateKernel) {
	t.Helper()
	s, owner, ops, events, _, child := batchFixture(t)
	s.input = 0
	policy, _ := providerFixture()
	kernel := &soleInputCandidateKernel{endpointCandidateKernelFixture: endpointCandidateFixture()}
	var err error
	s, err = soleInputProfileSpec(s, new(serviceready.SoleInput), policy, kernel)
	if err != nil {
		t.Fatal(err)
	}
	provider := s.producer.(*protectedProfileProvider)
	ops.moveSoleInput = func(context.Context, *serviceready.SoleInput) (uintptr, error) { return 31, nil }
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		if _, _, err := prepareProfilePipe(ctx, provider); err != nil {
			return nil, err
		}
		return &memoryBatchProfile{events: events}, nil
	}
	return s, owner, ops, events, child, kernel
}

func TestSoleInputBatchRetainsInvalidOrBorrowedReturnWithoutClosing(t *testing.T) {
	for _, kind := range []string{"pseudo", "helper-token", "service-token"} {
		t.Run(kind, func(t *testing.T) {
			s, owner, ops, _, _, kernel := soleInputSetup(t)
			value := ^uintptr(0)
			if kind == "helper-token" {
				value = s.launch.primaryToken
			}
			if kind == "service-token" {
				value = 71
				ops.scope, _ = newBatchScope(value, batchScopeOps{threadID: func() uint32 { return 7 }, clean: func() error { return nil }, verify: func(context.Context, uintptr) (batchServiceScope, error) {
					return &memoryServiceScope{events: new([]string)}, nil
				}})
			}
			ops.moveSoleInput = func(context.Context, *serviceready.SoleInput) (uintptr, error) { return value, nil }
			closes := 0
			ops.closeHandle = func(uintptr) error { closes++; return nil }
			b, err := prepareBatch(s, owner, ops)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.Start(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
				t.Error("invalid source return was not retained as unknown", err)
			}
			if b.input != value || closes != 0 || kernel.opened != 0 {
				t.Fatal("invalid/borrowed return was closed, lost, or queried", b.input, closes, kernel.opened)
			}
		})
	}
}

func TestSoleInputBatchFatalMoveRetiresWithoutNativeCleanup(t *testing.T) {
	for _, kind := range []string{"zero", "owned", "pseudo", "helper-token"} {
		s, owner, ops, _, _, kernel := soleInputSetup(t)
		value := uintptr(31)
		if kind == "zero" {
			value = 0
		}
		if kind == "pseudo" {
			value = ^uintptr(0)
		}
		if kind == "helper-token" {
			value = s.launch.primaryToken
		}
		ops.moveSoleInput = func(context.Context, *serviceready.SoleInput) (uintptr, error) { return value, serviceready.ErrFatal }
		closes := 0
		ops.closeHandle = func(uintptr) error { closes++; return nil }
		b, err := prepareBatch(s, owner, ops)
		if err != nil {
			t.Fatal(err)
		}
		if err = b.Start(requestContext(t)); !errors.Is(err, ErrFatal) {
			t.Error("fatal source return shrank to ordinary denial", err)
		}
		if b.input != value || closes != 0 || kernel.opened != 0 {
			t.Fatal("fatal source resumed native cleanup/query", b.input, closes, kernel.opened)
		}
		if err = b.Close(requestContext(t)); !errors.Is(err, ErrFatal) {
			t.Fatal("fatal source owner revived", err)
		}
	}
}

type soleInputAliasProfile struct {
	*memoryBatchProfile
	reader uintptr
}

func (p *soleInputAliasProfile) childReader() uintptr { return p.reader }

type soleInputAliasReport struct {
	*memoryBatchReport
	writer uintptr
}

func (p *soleInputAliasReport) childWriter() uintptr { return p.writer }

func TestSoleInputBatchRejectsProfileReportAliasesBeforeHelperStart(t *testing.T) {
	for _, kind := range []string{"profile-input", "profile-token", "profile-report", "report-input", "report-token", "report-profile", "profile-input-fatal", "report-input-fatal"} {
		t.Run(kind, func(t *testing.T) {
			s, owner, ops, events, _, _ := soleInputSetup(t)
			reader, writer := uintptr(63), uintptr(61)
			var profileErr, reportErr error
			switch kind {
			case "profile-input":
				reader = 31
			case "profile-token":
				reader = s.launch.primaryToken
			case "profile-report":
				reader = writer
			case "report-input":
				writer = 31
			case "report-token":
				writer = s.launch.primaryToken
			case "report-profile":
				writer = reader
			case "profile-input-fatal":
				reader, profileErr = 31, ErrFatal
			case "report-input-fatal":
				writer, reportErr = 31, ErrFatal
			}
			ops.newProfile = func(context.Context) (batchProfile, error) {
				return &soleInputAliasProfile{&memoryBatchProfile{events: events}, reader}, profileErr
			}
			ops.newReport = func(context.Context, reportConfig) (batchReport, error) {
				return &soleInputAliasReport{&memoryBatchReport{events: events}, writer}, reportErr
			}
			starts, closes := 0, 0
			ops.start = func(context.Context, nativeLaunchSpec) (batchChild, error) { starts++; return nil, ErrDenied }
			ops.closeHandle = func(uintptr) error { closes++; return nil }
			b, err := prepareBatch(s, owner, ops)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.Start(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
				t.Error("aliased ownership was not retained as unknown", err)
			}
			if (profileErr != nil || reportErr != nil) && !errors.Is(err, ErrFatal) {
				t.Error("alias classification lost constructor fatal", err)
			}
			if starts != 0 || closes != 0 || b.input != 31 {
				t.Fatal("aliased handles reached launch or duplicate cleanup", starts, closes, b.input)
			}
		})
	}
}

type soleInputCandidateKernel struct {
	*endpointCandidateKernelFixture
	inputs []uintptr
}

func (k *soleInputCandidateKernel) endpoint(h uintptr) (uint32, error) {
	k.inputs = append(k.inputs, h)
	if h != 31 {
		return 0, ErrDenied
	}
	return k.endpointCandidateKernelFixture.endpoint(h)
}

func TestSoleInputBatchBindsMovedInputToExistingCandidateLease(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	s.input = 0
	policy, _ := providerFixture()
	kernel := &soleInputCandidateKernel{endpointCandidateKernelFixture: endpointCandidateFixture()}
	input := new(serviceready.SoleInput)
	s, err := soleInputProfileSpec(s, input, policy, kernel)
	if err != nil {
		t.Fatal(err)
	}
	provider := s.producer.(*protectedProfileProvider)
	moves := 0
	ops.moveSoleInput = func(_ context.Context, actual *serviceready.SoleInput) (uintptr, error) {
		if actual != input {
			t.Fatal("source replaced")
		}
		moves++
		return 31, nil
	}
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		if _, _, err := prepareProfilePipe(ctx, provider); err != nil {
			return nil, err
		}
		return &memoryBatchProfile{events: events}, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if moves != 0 || kernel.opened != 0 || len(kernel.inputs) != 0 || b.input != 0 {
		t.Fatal("prepare acquired source")
	}
	if err = b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if moves != 1 || !reflect.DeepEqual(kernel.inputs, []uintptr{31, 31}) {
		t.Fatal("different input reached candidate acquisition", moves, kernel.inputs)
	}
	if _, err := b.Collect(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kernel.closed, []uintptr{12, 11}) {
		t.Fatal("candidate refs not reclaimed", kernel.closed)
	}
}

func TestSoleInputBatchPreStartCloseLeavesSourceWithEndpointOwner(t *testing.T) {
	s, owner, ops, events, _, kernel := soleInputSetup(t)
	moves := 0
	ops.moveSoleInput = func(context.Context, *serviceready.SoleInput) (uintptr, error) { moves++; return 31, nil }
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if moves != 0 || b.input != 0 || b.spec.soleInput != s.soleInput || kernel.opened != 0 || len(kernel.inputs) != 0 || len(*events) != 0 {
		t.Fatal("prestart close moved/queried/closed Endpoint-owned source", moves, *events)
	}
	if err = b.Start(requestContext(t)); err == nil {
		t.Fatal("closed typed source owner started")
	}
}

func TestSoleInputBatchRetainsPartialAndCancelledSourceReturnsBeforeCleanup(t *testing.T) {
	for _, kind := range []string{"partial-error", "cancel", "stop", "unknown-close"} {
		t.Run(kind, func(t *testing.T) {
			s, owner, ops, _, child, kernel := soleInputSetup(t)
			ctx, cancel := context.WithCancel(requestContext(t))
			defer cancel()
			moves := 0
			ops.moveSoleInput = func(context.Context, *serviceready.SoleInput) (uintptr, error) {
				moves++
				if kind == "cancel" {
					cancel()
					return 31, nil
				}
				if kind == "stop" {
					_ = owner.owner.Revoke()
					return 31, nil
				}
				return 31, ErrDenied
			}
			var b *nativeBatch
			closeUnknown := kind == "unknown-close"
			var closed []uintptr
			ops.closeHandle = func(h uintptr) error {
				if b.input != h {
					t.Error("source return was not retained before cleanup")
				}
				closed = append(closed, h)
				if closeUnknown {
					return ErrUnconfirmed
				}
				return nil
			}
			var err error
			b, err = prepareBatch(s, owner, ops)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.Start(ctx); err == nil {
				t.Fatal("partial/stopped source started")
			}
			if closeUnknown {
				if b.input != 31 || !errors.Is(err, ErrUnconfirmed) {
					t.Fatal("unconfirmed close lost source", b.input, err)
				}
				closeUnknown = false
			}
			if err = b.Close(requestContext(t)); err != nil {
				t.Fatal(err)
			}
			if b.input != 0 || moves != 1 || kernel.opened != 0 || len(kernel.inputs) != 0 || child.resumeCalled || len(closed) == 0 {
				t.Fatal("late source escaped cleanup or continued acquisition", b.input, moves, closed)
			}
		})
	}
}

func TestSoleInputBatchCloseWaitsForActualMoveReturn(t *testing.T) {
	s, owner, ops, _, child, kernel := soleInputSetup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	moves := 0
	ops.moveSoleInput = func(context.Context, *serviceready.SoleInput) (uintptr, error) {
		moves++
		close(entered)
		<-release
		return 31, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- b.Start(requestContext(t)) }()
	<-entered
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = b.Close(cleanup)
	cancel()
	if !errors.Is(err, ErrUnconfirmed) {
		close(release)
		<-started
		t.Fatal("Move nonreturn acknowledged", err)
	}
	if b.input != 0 || kernel.opened != 0 {
		close(release)
		<-started
		t.Fatal("Move completion was fabricated")
	}
	close(release)
	if err = <-started; err == nil {
		t.Fatal("late return revived stopped Start")
	}
	if moves != 1 || b.input != 0 || child.resumeCalled || kernel.opened != 0 {
		t.Fatal("late returned source was lost or used", moves, b.input)
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestSoleInputBatchMovePanicRetainsUnconfirmedSource(t *testing.T) {
	s, owner, ops, events, _, kernel := soleInputSetup(t)
	ops.moveSoleInput = func(context.Context, *serviceready.SoleInput) (uintptr, error) {
		panic("source reference outcome unknown")
	}
	cleanCalls := 0
	ops.clean = func() error { cleanCalls++; return nil }
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Start(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if err = b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unknown source acknowledged", err)
	}
	if b.spec.soleInput != s.soleInput || b.input != 0 || kernel.opened != 0 || cleanCalls != 0 || len(*events) != 0 {
		t.Fatal("panic source was discarded or native cleanup resumed")
	}
}

func TestSoleInputBatchProductionMoveUsesConcreteSourceMethod(t *testing.T) {
	s, owner, ops, events, _, kernel := soleInputSetup(t)
	ops.moveSoleInput = moveNativeSoleInput // zero SoleInput の実 method は拒否する。
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Start(requestContext(t)); err == nil {
		t.Fatal("typed zero source became authentication/move proof")
	}
	if b.input != 0 || kernel.opened != 0 || len(*events) != 0 {
		t.Fatal("invalid concrete source acquired resources")
	}
}

func TestSoleInputBatchPolicyAndRawMixRejectBeforeAcquisition(t *testing.T) {
	for _, kind := range []string{"raw-input", "nil-source", "authentication", "launch", "cleanup", "producer"} {
		s, _, _, _, _, _ := batchFixture(t)
		s.input = 0
		policy, _ := providerFixture()
		source := new(serviceready.SoleInput)
		kernel := endpointCandidateFixture()
		switch kind {
		case "raw-input":
			s.input = 31
		case "nil-source":
			source = nil
		case "authentication":
			policy.primary.authentication = 0
		case "launch":
			policy.material.launchID = "other"
		case "cleanup":
			policy.material.cleanup++
		case "producer":
			s.producer = profilePipeTestProducer()
		}
		if _, err := soleInputProfileSpec(s, source, policy, kernel); err == nil {
			t.Errorf("%s accepted", kind)
		}
		if kernel.opened != 0 || kernel.endpointCalls != 0 {
			t.Fatal("invalid policy/source queried candidate")
		}
	}
}

func TestSoleInputCandidateBindIsSingleUseAndCannotFollowClose(t *testing.T) {
	for _, kind := range []string{"bound", "closed", "queried-before-bind"} {
		kernel := endpointCandidateFixture()
		lease := &endpointCandidateLease{api: kernel}
		switch kind {
		case "bound":
			if err := lease.bindInput(31); err != nil {
				t.Fatal(err)
			}
		case "closed":
			if err := lease.close(requestContext(t)); err != nil {
				t.Fatal(err)
			}
		case "queried-before-bind":
			if _, err := lease.inspect(requestContext(t)); err == nil {
				t.Fatal("unbound lease queried")
			}
		}
		if err := lease.bindInput(32); err == nil {
			t.Fatalf("%s lease rebound", kind)
		}
		if kernel.opened != 0 || kernel.endpointCalls != 0 {
			t.Fatal("pure bind/close queried candidate")
		}
	}
}
