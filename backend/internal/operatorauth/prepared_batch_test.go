package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func TestPreparedBatchMatchesOnlyItsOriginalIssuer(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	original := owner.issuer
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	foreign := &ephemeral.Issuer{}
	owner.issuer = foreign
	clone := new(Batch)
	reflect.ValueOf(clone).Elem().Set(reflect.ValueOf(b).Elem())
	for _, test := range []struct {
		name   string
		batch  *Batch
		issuer *ephemeral.Issuer
		want   bool
	}{
		{"original", b, original, true},
		{"foreign", b, foreign, false},
		{"nil-issuer", b, nil, false},
		{"nil-batch", nil, original, false},
		{"zero-batch", &Batch{}, original, false},
		{"copied-batch", clone, original, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.batch.MatchesIssuer(test.issuer); got != test.want {
				t.Fatalf("issuer match = %v, want %v", got, test.want)
			}
		})
	}
	if len(*events) != 0 {
		t.Fatal("issuer comparison acquired resources", *events)
	}
}

func TestPreparedBatchMissingIssuerCannotAcquireResources(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	ops.parent = func(context.Context) error { *events = append(*events, "parent"); return nil }
	ops.clean = func() error { *events = append(*events, "clean"); return nil }
	missing := batchOwner{owner: owner.owner}
	if b, err := prepareBatch(s, missing, ops); b != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("missing issuer prepared", b != nil, err)
	}
	if len(*events) != 0 {
		t.Fatal("missing issuer acquired resources", *events)
	}
}

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
	if !owner.issuer.Snapshot().Closed {
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
	if len(*events) != 0 || !owner.issuer.Snapshot().Closed {
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
	entered, release := make(chan struct{}), make(chan struct{})
	revoked := owner.owner.StopRequested()
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
	if b, err := prepareNativeBatch(s, nil, 0, owner); b != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("missing protected source prepared", b, err)
	}
	if len(*events) != 0 {
		t.Fatal("missing native source acquired resources", *events)
	}
}

func TestPreparedBatchConcreteOwnerRevokeRejectsNativeAdmission(t *testing.T) {
	s, owner, ops, events, _, _ := batchFixture(t)
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(requestContext(t)); err == nil {
		t.Fatal("revoked owner started")
	}
	for _, event := range *events {
		if event == "profile" || event == "report" || event == "suspended" {
			t.Fatal("revoked owner acquired native resources", *events)
		}
	}
	if !owner.issuer.Snapshot().Closed {
		t.Fatal("batch retained a label instead of the actual issuer")
	}
}

func TestPreparedBatchConcreteOwnerRevokeRetainsLateConstructor(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	original := ops.newProfile
	returned := false
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		close(entered)
		<-release
		p, err := original(ctx)
		returned = true
		return p, err
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- b.Start(requestContext(t)) }()
	<-entered
	// Revocation must finish while a pre-admitted native call is still in flight.
	if err := owner.owner.Revoke(); err != nil {
		close(release)
		<-started
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = b.Close(short)
	cancel()
	if !errors.Is(err, ErrUnconfirmed) {
		close(release)
		<-started
		t.Fatal("late owner was acknowledged early", err)
	}
	close(release)
	if err := <-started; err == nil {
		t.Fatal("late constructor revived stopped batch")
	}
	if !returned || b.profile == nil {
		t.Fatal("late acquired owner was discarded")
	}
	if err := b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

type recheckBarrierChild struct {
	*memoryBatchChild
	entered chan struct{}
	release <-chan struct{}
}

func (c *recheckBarrierChild) recheck(ctx context.Context) error {
	close(c.entered)
	select {
	case <-c.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.memoryBatchChild.recheck(ctx)
}

type transferCountingProfile struct {
	*memoryBatchProfile
	transfers int
}

func (p *transferCountingProfile) sendAfterResume(ctx context.Context) error {
	p.transfers++
	return p.memoryBatchProfile.sendAfterResume(ctx)
}

func TestPreparedBatchRechecksAdmissionBeforeProfileTransferAfterLateRecheck(t *testing.T) {
	s, owner, ops, events, _, child := batchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	profile := &transferCountingProfile{memoryBatchProfile: &memoryBatchProfile{events: events}}
	ops.newProfile = func(context.Context) (batchProfile, error) { return profile, nil }
	originalStart := ops.start
	ops.start = func(c context.Context, l nativeLaunchSpec) (batchChild, error) {
		if _, err := originalStart(c, l); err != nil {
			return nil, err
		}
		return &recheckBarrierChild{memoryBatchChild: child, entered: entered, release: release}, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- b.Start(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("post-resume recheck did not enter memory barrier")
	}
	revokeErr := owner.owner.Revoke()
	close(release)
	select {
	case err := <-started:
		if err == nil {
			t.Fatal("revoked batch start succeeded")
		}
	case <-ctx.Done():
		t.Fatal("late recheck owner did not join")
	}
	if revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if profile.transfers != 0 {
		t.Fatal("profile transfer acquired after owner admission closed", profile.transfers)
	}
	if err := b.Close(requestContext(t)); err != nil {
		t.Fatal("same batch failed retained cleanup", err)
	}
}
