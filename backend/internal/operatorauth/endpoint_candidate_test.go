package operatorauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type endpointCandidateKernelFixture struct {
	pid                          uint32
	process, token               uintptr
	openProcessErr, openTokenErr error
	opened                       int
	closed                       []uintptr
	onProcess                    func()
	query                        *profileQueryKernelFixture
	endpointCalls                int
	endpointErr                  error
	closeErr                     error
	onSubject                    func()
}

func (k *endpointCandidateKernelFixture) subject() error {
	if k.onSubject != nil {
		k.onSubject()
	}
	return nil
}
func (k *endpointCandidateKernelFixture) endpoint(uintptr) (uint32, error) {
	k.endpointCalls++
	return k.pid, k.endpointErr
}
func (k *endpointCandidateKernelFixture) openProcess(uint32) (uintptr, error) {
	k.opened++
	if k.onProcess != nil {
		k.onProcess()
	}
	return k.process, k.openProcessErr
}
func (k *endpointCandidateKernelFixture) openToken(uintptr) (uintptr, error) {
	k.opened++
	return k.token, k.openTokenErr
}
func (k *endpointCandidateKernelFixture) queryKernel() profileQueryKernel { return k.query }
func (k *endpointCandidateKernelFixture) close(h uintptr) error {
	k.closed = append(k.closed, h)
	return k.closeErr
}
func endpointCandidateFixture() *endpointCandidateKernelFixture {
	_, q := providerFixture()
	return &endpointCandidateKernelFixture{pid: q.snapshot.pid, process: 11, token: 12, query: &profileQueryKernelFixture{state: q.snapshot}}
}

func TestEndpointCandidateAcquiresOnlyWhenRegisteredBatchStarts(t *testing.T) {
	k := endpointCandidateFixture()
	p, _ := providerFixture()
	s, owner, ops, events, _, _ := batchFixture(t)
	s, err := endpointProfileSpec(s, p, k)
	if err != nil {
		t.Fatal(err)
	}
	provider := s.producer.(*protectedProfileProvider)
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		if _, _, e := prepareProfilePipe(ctx, provider); e != nil {
			return nil, e
		}
		return &memoryBatchProfile{events: events}, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if k.opened != 0 {
		t.Fatal("candidate acquired before registration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if k.endpointCalls != 2 {
		t.Fatal("endpoint not checked around acquisition")
	}
	k.endpointErr = ErrDenied // BatchはStart後に親readをclose済み。番号の再queryは禁止。
	proof, err := b.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Collector().Consume(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if len(k.closed) != 2 || k.closed[0] != 12 || k.closed[1] != 11 {
		t.Fatalf("lease ownership lost: %v", k.closed)
	}
}

func TestEndpointCandidateRetainsPartialReferencesAndRejectsLateCancel(t *testing.T) {
	for _, stage := range []string{"process", "token", "cancel", "pid"} {
		t.Run(stage, func(t *testing.T) {
			k := endpointCandidateFixture()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch stage {
			case "process":
				k.openProcessErr = ErrDenied
			case "token":
				k.openTokenErr = ErrDenied
			case "cancel":
				k.onProcess = cancel
			case "pid":
				k.onProcess = func() { k.pid++ }
			}
			lease, err := newEndpointCandidateLease(31, k)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = lease.inspect(ctx); err == nil {
				t.Fatal("invalid acquisition accepted")
			}
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err = lease.close(cleanup); err != nil {
				t.Fatal(err)
			}
			want := 2
			if stage == "process" || stage == "cancel" {
				want = 1
			}
			if len(k.closed) != want {
				t.Fatalf("partial references lost: %v", k.closed)
			}
			if stage == "cancel" && k.opened != 1 {
				t.Fatal("token opened after cancellation")
			}
		})
	}
}

func TestEndpointCandidateUnknownCloseCannotReviveOrComplete(t *testing.T) {
	k := endpointCandidateFixture()
	lease, err := newEndpointCandidateLease(31, k)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = lease.inspect(ctx); err != nil {
		t.Fatal(err)
	}
	k.closeErr = ErrUnconfirmed
	if err = lease.close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if lease.token == 0 || lease.process == 0 {
		t.Fatal("close failure lost owner")
	}
	if _, err = lease.inspect(ctx); err == nil {
		t.Fatal("closed lease revived")
	}
	k.closeErr = nil
	if err = lease.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointCandidateConstructorPanicRetainsUnknown(t *testing.T) {
	k := endpointCandidateFixture()
	k.onProcess = func() { panic("synthetic constructor") }
	lease, err := newEndpointCandidateLease(31, k)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = lease.inspect(ctx); err == nil {
		t.Fatal("constructor panic accepted")
	}
	if err = lease.close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unknown acquisition completed", err)
	}
}

func TestEndpointCandidateCancellationDuringSubjectDoesNotQueryEndpoint(t *testing.T) {
	k := endpointCandidateFixture()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k.onSubject = cancel
	lease, err := newEndpointCandidateLease(31, k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lease.inspect(ctx); err == nil {
		t.Fatal("cancelled admission accepted")
	}
	if k.endpointCalls != 0 {
		t.Fatal("endpoint queried after cancellation")
	}
}

func TestEndpointCandidateStopWaitsForLateProcessAcquisition(t *testing.T) {
	k := endpointCandidateFixture()
	entered, release := make(chan struct{}), make(chan struct{})
	k.onProcess = func() { close(entered); <-release }
	p, _ := providerFixture()
	s, owner, ops, events, _, child := batchFixture(t)
	s, err := endpointProfileSpec(s, p, k)
	if err != nil {
		t.Fatal(err)
	}
	provider := s.producer.(*protectedProfileProvider)
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		if _, _, e := prepareProfilePipe(ctx, provider); e != nil {
			return nil, e
		}
		return &memoryBatchProfile{events: events}, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Start(ctx) }()
	<-entered
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	err = b.Close(cleanup)
	stop()
	if !errors.Is(err, ErrUnconfirmed) {
		close(release)
		<-done
		t.Fatal("pending constructor was acknowledged", err)
	}
	close(release)
	if err = <-done; err == nil {
		t.Fatal("late acquisition started helper")
	}
	if child.resumeCalled || len(k.closed) != 1 || k.closed[0] != 11 {
		t.Fatalf("late reference lost or resumed: %v %v", child.resumeCalled, k.closed)
	}
}

func TestEndpointProfileSpecRejectsMissingOrOtherLaunchPolicyBeforeQuery(t *testing.T) {
	for _, field := range []string{"authentication", "launch", "cleanup", "producer"} {
		t.Run(field, func(t *testing.T) {
			k := endpointCandidateFixture()
			p, _ := providerFixture()
			s, _, _, _, _, _ := batchFixture(t)
			switch field {
			case "authentication":
				p.primary.authentication = 0
			case "launch":
				p.material.launchID = "bad"
			case "cleanup":
				p.material.cleanup++
			case "producer":
				s.producer = profilePipeTestProducer()
			}
			if _, err := endpointProfileSpec(s, p, k); err == nil || k.opened != 0 {
				t.Fatal("invalid or noncanonical source accepted", err)
			}
		})
	}
}
