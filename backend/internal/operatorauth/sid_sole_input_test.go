package operatorauth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// Only OS operations and the protected receiver source are memory fixtures.
// Typed ownership, profile delivery, message authentication and sealed consume
// run through the existing Batch/Helper/Collector implementations.
func TestSIDSoleInputBatchMovesAndConsumesAuthenticatedRead(t *testing.T) {
	ctx, receiver, _ := receiverSetup(t)
	data := make(chan []byte, 1)
	api := &profileSyncFixture{transferFn: func(kind profileSyncKind, _ uintptr, b []byte) (uint32, error) {
		if kind == profileSyncWrite {
			data <- append([]byte(nil), b...)
			return uint32(len(b)), nil
		}
		chunk, ok := <-data
		if !ok {
			return 0, errProfileSyncEOF
		}
		return uint32(copy(b, chunk)), nil
	}}
	receiver.ops.start = func(c context.Context, h uintptr, b []byte) (*profileSyncOwner, error) {
		return startProfileSync(c, api, profileSyncRead, h, b)
	}
	kernel := &profilePipeKernelFixture{inheritFn: func(uintptr) (uintptr, error) { return 77, nil }, closeFn: func(h uintptr) error {
		if h == 52 {
			close(data)
		}
		return nil
	}}
	s, owner, ops, events, report, child := batchFixture(t)
	input := new(serviceready.SoleInput)
	s.input, s.soleInput, s.sidPolicy = 0, input, fixedSIDPolicy()
	s.report.guestSID = s.sidPolicy.GuestSID
	producer, err := newSIDProfileProvider(s.launch.launchID, s.sidPolicy, s.launch.cleanup)
	if err != nil {
		t.Fatal(err)
	}
	s.producer = producer
	moves, registered := 0, false
	ops.moveSoleInput = func(_ context.Context, got *serviceready.SoleInput) (uintptr, error) {
		if got != input || !registered {
			t.Error("Move replaced the source or preceded owner registration")
		}
		moves++
		return 31, nil
	}
	ops.newProfile = func(c context.Context) (batchProfile, error) {
		if moves != 1 {
			t.Error("profile acquisition preceded sole input transfer")
		}
		return newProfilePipe(c, producer, kernel, api)
	}
	h, _, _, published := sidHelperFixture()
	ops.newReport = func(context.Context, reportConfig) (batchReport, error) {
		return &sidDeliveredReport{report, published}, nil
	}
	finished := make(chan error, 1)
	ops.start = func(_ context.Context, l nativeLaunchSpec) (batchChild, error) {
		if l.operatorRead != 32 || l.profileRead != 77 || l.launchID != s.launch.launchID {
			return nil, ErrDenied
		}
		return &deliveryChild{child, func() { go func() { finished <- receiver.run(ctx, h.ops) }() }}, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal("typed SID batch could not be prepared", err)
	}
	if moves != 0 || len(*events) != 0 || !b.MatchesSoleInput(input) || b.MatchesSoleInput(new(serviceready.SoleInput)) || !b.MatchesOwner(owner.owner, owner.issuer) {
		t.Fatal("preparation acquired resources or lost exact provenance")
	}
	registered = true // the memory Service owner now retains this exact Batch
	if err = b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
	case <-ctx.Done():
		t.Fatal("helper did not join")
	}
	if err != nil {
		t.Fatal(err)
	}
	read, err := b.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frame, binding, err := b.Collector().ConsumeAuthenticated(ctx, read)
	if err != nil || string(frame) != string(requestFrame()) || !binding.Matches(b.Collector(), owner.owner, owner.issuer) || !binding.MatchesSIDPolicy(s.sidPolicy.OperatorSID, s.sidPolicy.GuestSID) {
		t.Fatal("typed source lost authenticated ownership", err)
	}
	if err = b.Close(requestContext(t)); err != nil || moves != 1 || !b.MatchesSoleInput(input) {
		t.Fatal("same batch did not finish the single transfer", err, moves)
	}
}

func sidSoleInputSetup(t *testing.T) (nativeBatchSpec, batchOwner, nativeBatchOps, *[]string) {
	t.Helper()
	s, owner, ops, events, _, _ := batchFixture(t)
	s.input, s.report.guestSID = 0, fixedSIDPolicy().GuestSID
	s, err := soleInputSIDSpec(s, new(serviceready.SoleInput), fixedSIDPolicy())
	if err != nil {
		t.Fatal(err)
	}
	ops.moveSoleInput = func(_ context.Context, input *serviceready.SoleInput) (uintptr, error) {
		if input != s.soleInput {
			t.Error("typed source replaced")
		}
		return 31, nil
	}
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		if _, _, err := prepareProfilePipe(ctx, s.producer); err != nil {
			return nil, err
		}
		*events = append(*events, "profile")
		return &memoryBatchProfile{events: events}, nil
	}
	return s, owner, ops, events
}

func TestSIDSoleInputBatchRetainsPartialMoveUntilSameOwnerCloses(t *testing.T) {
	s, owner, ops, _ := sidSoleInputSetup(t)
	moves, closes, recoverable := 0, 0, false
	ops.moveSoleInput = func(_ context.Context, input *serviceready.SoleInput) (uintptr, error) {
		if input != s.soleInput {
			t.Error("partial move changed source")
		}
		moves++
		return 31, ErrDenied
	}
	ops.closeHandle = func(h uintptr) error {
		if h != 31 {
			t.Error("partial move closed a foreign handle", h)
		}
		closes++
		if !recoverable {
			return ErrUnconfirmed
		}
		return nil
	}
	ops.newProfile = func(context.Context) (batchProfile, error) {
		t.Error("failed Move acquired profile resources")
		return nil, ErrDenied
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Start(requestContext(t)); !errors.Is(err, ErrUnconfirmed) || b.input != 31 || moves != 1 || closes != 1 || !owner.issuer.Snapshot().Closed {
		t.Fatal("partial return lost ownership or cleanup uncertainty", err, moves, closes, b.input)
	}
	if read, err := b.Collect(requestContext(t)); err == nil || read != nil {
		t.Fatal("failed partial move produced an authenticated read")
	}
	recoverable = true
	if err = b.Close(requestContext(t)); err != nil || b.input != 0 || closes != 2 || !b.MatchesSoleInput(s.soleInput) {
		t.Fatal("same owner did not retry actual input close", err, closes, b.input)
	}
	if err = b.Start(requestContext(t)); !errors.Is(err, ErrDenied) || moves != 1 {
		t.Fatal("closed partial move restarted", err, moves)
	}
}

func TestSIDSoleInputBatchRejectsCancellationAndRevocationAroundMove(t *testing.T) {
	for _, phase := range []string{"cancel-before", "revoke-before", "close-before", "cancel-during", "revoke-during"} {
		t.Run(phase, func(t *testing.T) {
			s, owner, ops, _ := sidSoleInputSetup(t)
			ctx, cancel := context.WithCancel(requestContext(t))
			defer cancel()
			moves, closes := 0, 0
			stop := func() {
				if phase == "cancel-before" || phase == "cancel-during" {
					cancel()
				} else if err := owner.owner.Revoke(); err != nil {
					t.Error(err)
				}
			}
			ops.moveSoleInput = func(_ context.Context, input *serviceready.SoleInput) (uintptr, error) {
				if input != s.soleInput {
					t.Error("canceled transfer changed input")
				}
				moves++
				stop()
				return 31, nil
			}
			ops.newProfile = func(context.Context) (batchProfile, error) {
				t.Error("canceled/revoked transfer acquired profile")
				return nil, ErrDenied
			}
			ops.closeHandle = func(h uintptr) error {
				if h != 31 {
					t.Error("closed foreign input")
				}
				closes++
				return nil
			}
			b, err := prepareBatch(s, owner, ops)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			switch phase {
			case "cancel-before", "revoke-before":
				stop()
			case "close-before":
				if err = b.Close(requestContext(t)); err != nil {
					t.Fatal(err)
				}
			default:
				want = 1
			}
			if err = b.Start(ctx); err == nil || moves != want || closes != want || b.input != 0 {
				t.Fatal("stop lost transferred input or allowed new acquisition", err, moves, closes, b.input)
			}
			if read, err := b.Collect(requestContext(t)); read != nil || err == nil {
				t.Fatal("stopped move produced a read")
			}
			if err = b.Close(requestContext(t)); err != nil || closes != want || !b.MatchesSoleInput(s.soleInput) {
				t.Fatal("stop changed cleanup ownership", err, closes)
			}
		})
	}
}

func TestSIDSoleInputBatchCloseWaitsForPendingMove(t *testing.T) {
	s, owner, ops, _ := sidSoleInputSetup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var moves, closes atomic.Int32
	ops.moveSoleInput = func(_ context.Context, input *serviceready.SoleInput) (uintptr, error) {
		if input != s.soleInput {
			t.Error("pending Move changed source")
		}
		moves.Add(1)
		close(entered)
		<-release
		return 31, nil
	}
	ops.closeHandle = func(h uintptr) error {
		if h != 31 {
			t.Error("late Move closed foreign input")
		}
		closes.Add(1)
		return nil
	}
	ops.newProfile = func(context.Context) (batchProfile, error) {
		t.Error("late Move acquired profile")
		return nil, ErrDenied
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestContext(t)
	done := make(chan error, 1)
	go func() { done <- b.Start(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("Move not entered")
	}
	short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = b.Close(short)
	end()
	if !errors.Is(err, ErrUnconfirmed) || closes.Load() != 0 || !owner.issuer.Snapshot().Closed || !b.MatchesSoleInput(s.soleInput) {
		t.Fatal("pending constructor became a cleanup acknowledgement", err)
	}
	unblock()
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("late Move did not return")
	}
	if err == nil || b.input != 0 || closes.Load() != 1 {
		t.Fatal("late input return escaped same-owner cleanup", err, b.input, closes.Load())
	}
	if err = b.Close(requestContext(t)); err != nil || moves.Load() != 1 || closes.Load() != 1 {
		t.Fatal("cleanup retried Move or double-closed input", err)
	}
}

func TestSIDSoleInputBatchStartIsSingleUseAndLegacyStillQueries(t *testing.T) {
	for _, mode := range []string{"sid", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			var s nativeBatchSpec
			var owner batchOwner
			var ops nativeBatchOps
			var candidate *soleInputCandidateKernel
			if mode == "sid" {
				s, owner, ops, _ = sidSoleInputSetup(t)
				if s.inputLease != nil {
					t.Fatal("SID policy installed a legacy candidate source")
				}
			} else {
				s, owner, ops, _, _, candidate = soleInputSetup(t)
			}
			moves := 0
			originalMove := ops.moveSoleInput
			ops.moveSoleInput = func(ctx context.Context, input *serviceready.SoleInput) (uintptr, error) {
				moves++
				return originalMove(ctx, input)
			}
			b, err := prepareBatch(s, owner, ops)
			if err != nil || moves != 0 {
				t.Fatal("pure preparation failed or moved input", err)
			}
			if err = b.Start(requestContext(t)); err != nil {
				t.Fatal(err)
			}
			if err = b.Start(requestContext(t)); !errors.Is(err, ErrDenied) || moves != 1 {
				t.Fatal("second Start repeated Move", err, moves)
			}
			if candidate != nil {
				if candidate.opened == 0 || len(candidate.inputs) != 2 || candidate.inputs[0] != 31 || candidate.inputs[1] != 31 {
					t.Fatal("legacy path skipped exact candidate query", candidate.inputs)
				}
				read, err := b.Collect(requestContext(t))
				if err != nil {
					t.Fatal(err)
				}
				frame, err := b.Collector().Consume(requestContext(t), read)
				if err != nil || string(frame) != string(requestFrame()) {
					t.Fatal("legacy exact policy path changed", err)
				}
			}
			if err = b.Close(requestContext(t)); err != nil || !b.MatchesSoleInput(s.soleInput) {
				t.Fatal("single-use batch lost input provenance", err)
			}
			if err = b.Start(requestContext(t)); !errors.Is(err, ErrDenied) || moves != 1 {
				t.Fatal("closed batch repeated transfer", err, moves)
			}
		})
	}
}

func TestSIDSoleInputSpecRejectsMixedSourcesAndKeepsModeSpecificLease(t *testing.T) {
	for _, kind := range []string{"nil-input", "raw-input", "typed-input", "candidate", "producer", "prior-policy", "guest", "empty-policy"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, events, _, _ := batchFixture(t)
			s.input, s.report.guestSID = 0, fixedSIDPolicy().GuestSID
			input, policy := new(serviceready.SoleInput), fixedSIDPolicy()
			switch kind {
			case "nil-input":
				input = nil
			case "raw-input":
				s.input = 31
			case "typed-input":
				s.soleInput = input
			case "candidate":
				s.inputLease = &endpointCandidateLease{api: endpointCandidateFixture()}
			case "producer":
				s.producer, _ = newSIDProfileProvider(s.launch.launchID, policy, s.launch.cleanup)
			case "prior-policy":
				s.sidPolicy = policy
			case "guest":
				s.report.guestSID = policy.OperatorSID
			case "empty-policy":
				policy = SIDPolicy{}
			}
			if _, err := soleInputSIDSpec(s, input, policy); !errors.Is(err, ErrUnconfigured) || len(*events) != 0 {
				t.Fatal("mixed source prepared or acquired resources", err)
			}
		})
	}
	for _, mode := range []string{"sid-with-candidate", "legacy-without-candidate"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, ops, events := sidSoleInputSetup(t)
			if mode == "sid-with-candidate" {
				s.inputLease = &endpointCandidateLease{api: endpointCandidateFixture()}
			} else {
				s, owner, ops, events, _, _ = soleInputSetup(t)
				s.inputLease = nil
			}
			if b, err := prepareBatch(s, owner, ops); b != nil || !errors.Is(err, ErrUnconfigured) || len(*events) != 0 {
				t.Fatal("mode-specific source contract bypassed", err)
			}
		})
	}
}
