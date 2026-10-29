package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPreparedBatchKeepsCollectorWithoutAcquiringBeforeStart(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	b, err := prepareBatch(s, owner, ops)
	if err != nil || b == nil || b.Collector() == nil {
		t.Fatal("prepare failed", err)
	}
	collector := b.Collector()
	if len(*events) != 0 {
		t.Fatal("prepare performed native work", *events)
	}
	if err = b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if b.Collector() != collector {
		t.Fatal("start replaced collector owner")
	}
	if err = b.Start(requestContext(t)); err == nil {
		t.Fatal("start admitted twice")
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedBatchCloseDuringConstructorRetainsOwnerUntilStartAcknowledges(t *testing.T) {
	s, owner, ops, events, _, child := batchFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	original := ops.start
	ops.start = func(ctx context.Context, launch nativeLaunchSpec) (batchChild, error) {
		close(entered)
		<-release // Simulate an acquisition that returns its retained reference late.
		return original(ctx, launch)
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	collector := b.Collector()
	started := make(chan error, 1)
	go func() { started <- b.Start(requestContext(t)) }()
	<-entered
	cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = b.Close(cleanup)
	cancel()
	if !errors.Is(err, ErrUnconfirmed) {
		close(release)
		<-started
		t.Fatal("close claimed completion before constructor returned", err)
	}
	if len(*events) == 0 || (*events)[len(*events)-1] != "revoke" {
		close(release)
		<-started
		t.Fatal("cleanup ran before start completion or issuer was not revoked first", *events)
	}
	close(release)
	if err = <-started; err == nil {
		t.Fatal("cancelled constructor start succeeded")
	}
	if child.resumeCalled {
		t.Fatal("child resumed after close admission")
	}
	if b.Collector() != collector {
		t.Fatal("partial constructor replaced cleanup owner")
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal("same owner failed final cleanup", err)
	}
	found := false
	for _, event := range *events {
		if event == "terminate" {
			found = true
		}
	}
	if !found {
		t.Fatal("late returned child reference was not reclaimed", *events)
	}
	if err = b.Start(requestContext(t)); err == nil {
		t.Fatal("closed owner restarted")
	}
}

func TestPreparedBatchValueCopyCannotAcquireOrExposeOwner(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	clone := new(Batch)
	// Reflection models an external caller's value copy without a copylocks vet
	// warning. Copying before any operation ensures no locked mutex is involved.
	reflect.ValueOf(clone).Elem().Set(reflect.ValueOf(b).Elem())
	if clone.Collector() != nil {
		t.Fatal("value copy exposed original collector")
	}
	if err = clone.Start(requestContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal("value copy started", err)
	}
	if _, err = clone.Collect(requestContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal("value copy collected", err)
	}
	if err = clone.Close(requestContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal("value copy closed original owner", err)
	}
	if len(*events) != 0 {
		t.Fatal("value copy performed native work", *events)
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedBatchConstructorPanicCannotConfirmUnknownAcquisition(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	ops.newProfile = func(context.Context) (batchProfile, error) { panic("acquired but return outcome unknown") }
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Start(requestContext(t)); err == nil {
		t.Fatal("panicking acquisition started")
	}
	if err = b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unknown acquisition reported complete", err)
	}
}

func TestPreparedBatchFreshCollectCannotExtendStartRequest(t *testing.T) {
	for _, stage := range []string{"before-collect", "before-consume"} {
		t.Run(stage, func(t *testing.T) {
			s, owner, ops, _, _, _ := batchFixture(t)
			b, err := prepareBatch(s, owner, ops)
			if err != nil {
				t.Fatal(err)
			}
			request, cancel := context.WithCancel(requestContext(t))
			defer cancel()
			if err = b.Start(request); err != nil {
				t.Fatal(err)
			}
			if stage == "before-collect" {
				cancel()
				if _, err = b.Collect(requestContext(t)); err == nil {
					t.Fatal("fresh context revived cancelled start request")
				}
			} else {
				proof, err := b.Collect(requestContext(t))
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				if _, err = b.Collector().Consume(requestContext(t), proof); err == nil {
					t.Fatal("proof outlived start request cancellation")
				}
			}
			if err = b.Close(requestContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreparedBatchDirectCollectorCloseJoinsLateConstructor(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	original := ops.newProfile
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		close(entered)
		<-release
		return original(ctx)
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- b.Start(requestContext(t)) }()
	<-entered
	cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = b.Collector().Close(cleanup)
	cancel()
	if !errors.Is(err, ErrUnconfirmed) {
		close(release)
		<-started
		t.Fatal("collector completed before constructor ack", err)
	}
	if len(*events) != 1 || (*events)[0] != "revoke" {
		close(release)
		<-started
		t.Fatal("collector cleanup preceded acquisition ack", *events)
	}
	close(release)
	if err = <-started; err == nil {
		t.Fatal("direct collector close did not stop start")
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedBatchDirectCollectorPreStartCollectBurnsAdmission(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Collector().Collect(requestContext(t)); err == nil {
		t.Fatal("unstarted collector authenticated")
	}
	if err = b.Start(requestContext(t)); err == nil {
		t.Fatal("failed direct collector revived batch")
	}
	for _, event := range *events {
		if event == "profile" || event == "report" || event == "suspended" {
			t.Fatal("prestart collect acquired resources", *events)
		}
	}
}

func TestPreparedBatchDirectCollectDuringAcquisitionWaitsForConstructorAck(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	entered, release, revoked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	revoke := owner.revoke
	owner.revoke = func() error { err := revoke(); close(revoked); return err }
	original := ops.newProfile
	ops.newProfile = func(ctx context.Context) (batchProfile, error) { close(entered); <-release; return original(ctx) }
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	started, collected := make(chan error, 1), make(chan error, 1)
	go func() { started <- b.Start(requestContext(t)) }()
	<-entered
	go func() { _, err := b.Collector().Collect(requestContext(t)); collected <- err }()
	<-revoked
	close(release)
	if err = <-started; err == nil {
		t.Fatal("direct collect during acquisition revived start")
	}
	if err = <-collected; err == nil {
		t.Fatal("direct collect during acquisition returned proof")
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedBatchZeroValueAndMissingSourceFailClosed(t *testing.T) {
	var zero Batch
	if zero.Collector() != nil {
		t.Fatal("zero batch supplied collector")
	}
	if err := zero.Start(requestContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	if _, err := zero.Collect(requestContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	if err := zero.Close(requestContext(t)); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	s, owner, _, events, _, _ := batchFixture(t)
	if b, err := prepareNativeBatch(s, nil, owner); b != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("missing protected source prepared", b, err)
	}
	if len(*events) != 0 {
		t.Fatal("missing native source acquired resources", *events)
	}
}

func TestPreparedBatchIssuerRevokeFailureStaysSticky(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	attempts := 0
	owner.revoke = func() error { attempts++; return ErrDenied }
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
			t.Fatal("issuer failure reported complete", err)
		}
	}
	if attempts != 1 {
		t.Fatal("failed issuer revocation was retried as a fresh authority", attempts)
	}
	if err = b.Start(requestContext(t)); err == nil {
		t.Fatal("revocation failure revived admission")
	}
}

func TestPreparedBatchIssuerPanicCannotLeaveIssuedProofLive(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	owner.revoke = func() error { panic("issuer revocation outcome unknown") }
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	proof, err := b.Collector().Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("issuer panic confirmed cleanup", err)
	}
	if _, err = b.Collector().Consume(requestContext(t), proof); err == nil {
		t.Fatal("issuer panic left proof usable")
	}
	if err = b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("issuer panic was not sticky", err)
	}
}
