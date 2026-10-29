package serviceauthowner_test

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func TestOwnerRevokeClosesActualIssuerAndAdmission(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	var gate sync.Mutex
	writer, owner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	if !issuer.Snapshot().Closed {
		t.Fatal("actual issuer remains open")
	}
	if err := owner.Admit(ctx); err == nil {
		t.Fatal("closed owner admitted work")
	}
	gate.Lock()
	open := writer.OpenLocked()
	gate.Unlock()
	if open {
		t.Fatal("writer still publishes open admission")
	}
	select {
	case <-owner.StopRequested():
	default:
		t.Fatal("revoke did not notify service")
	}
	if err := owner.AwaitWorkload(ctx); err == nil {
		t.Fatal("revocation became workload cleanup")
	}
}

func TestOwnerRejectsZeroCopiesAndForeignWriter(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	var gate sync.Mutex
	writer, owner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	foreignWriter, foreignOwner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	copiedWriter, copiedOwner := *writer, *owner
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, bad := range []*serviceauthowner.Owner{nil, {}, &copiedOwner} {
		if bad.MatchesIssuer(issuer) || bad.Revoke() == nil || bad.Admit(ctx) == nil || bad.AwaitWorkload(ctx) == nil || bad.StopRequested() != nil {
			t.Fatal("invalid owner exposed capability")
		}
	}
	gate.Lock()
	if writer.MatchesLocked(foreignOwner, issuer) || foreignWriter.MatchesLocked(owner, issuer) || copiedWriter.OpenLocked() || copiedWriter.MatchesLocked(owner, issuer) || copiedWriter.RevokeLocked() == nil || copiedWriter.ConfirmWorkloadLocked() == nil {
		t.Error("copied/foreign writer accepted")
	}
	gate.Unlock()
	if issuer.Snapshot().Closed {
		t.Fatal("invalid copy revoked real owner")
	}
	if err := owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	gate.Lock()
	if err := foreignWriter.ConfirmWorkloadLocked(); err == nil {
		t.Error("foreign pair confirmed canonical workload")
	}
	gate.Unlock()
	short, end := context.WithTimeout(ctx, 5*time.Millisecond)
	defer end()
	if owner.AwaitWorkload(short) == nil {
		t.Fatal("foreign writer confirmed canonical owner")
	}
	gate.Lock()
	if err := writer.ConfirmWorkloadLocked(); err != nil {
		t.Error(err)
	}
	gate.Unlock()
	if err := owner.AwaitWorkload(ctx); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []*ephemeral.Issuer{nil, {}} {
		if w, o, err := serviceauthowner.New(invalid, &gate); w != nil || o != nil || err == nil {
			t.Fatal("invalid issuer configured owner")
		}
	}
	if w, o, err := serviceauthowner.New(issuer, nil); w != nil || o != nil || err == nil {
		t.Fatal("missing actual gate configured owner")
	}
}

func TestOwnerPendingRetryAndStickyFatalAreSeparateFromRevocation(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	var gate sync.Mutex
	writer, owner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	gate.Lock()
	if writer.ConfirmWorkloadLocked() == nil {
		t.Error("open owner confirmed workload")
	}
	gate.Unlock()
	if err := owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	short, end := context.WithTimeout(ctx, 5*time.Millisecond)
	if owner.AwaitWorkload(short) == nil {
		t.Fatal("revoke became workload cleanup")
	}
	end()
	fatal := errors.New("fixed workload fatal")
	waiter := make(chan error, 1)
	go func() { waiter <- owner.AwaitWorkload(ctx) }()
	gate.Lock()
	writer.NotifyPendingLocked()
	_ = writer.FatalWorkloadLocked(fatal)
	if writer.ConfirmWorkloadLocked() == nil {
		t.Error("fatal became confirmed")
	}
	gate.Unlock()
	if err := <-waiter; !errors.Is(err, fatal) {
		t.Fatal("waiter lost terminal outcome", err)
	}
	if err := owner.AwaitWorkload(ctx); !errors.Is(err, fatal) {
		t.Fatal("fatal not sticky", err)
	}
	if owner.Admit(ctx) == nil {
		t.Fatal("fatal revived admission")
	}
	if owner.Admit(context.Background()) == nil || owner.AwaitWorkload(context.Background()) == nil {
		t.Fatal("infinite wait accepted")
	}
}

func TestOwnerRevokeCommitsInflightIssuerWithoutConfirmingWorkload(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	var gate sync.Mutex
	writer, owner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	policy := ephemeral.ApprovedPolicy{PolicyDigest: strings.Repeat("a", 64), KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: now.Add(20 * time.Second), BackendLeaseCeiling: 3 * time.Second}
	challenge, err := issuer.Prepare(policy)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := issuer.Launch(ctx, challenge, func() time.Time { return now }, func(c context.Context, _ *supervisor.Authority) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		})
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("launch did not enter")
	}
	if err := owner.Revoke(); err != nil {
		t.Fatal("committed revocation treated as workload acknowledgement", err)
	}
	// The same real issuer still reports the documented in-flight sentinel:
	// Owner normalized only revocation, while the original worker remains live.
	if err := issuer.Close(); !errors.Is(err, ephemeral.ErrLaunchUnconfirmed) {
		t.Fatal("expected in-flight sentinel", err)
	}
	snapshot := issuer.Snapshot()
	if !snapshot.Closed || snapshot.HasAuthority {
		t.Fatal("issuer revocation not committed")
	}
	if owner.Admit(ctx) == nil {
		t.Fatal("in-flight close admitted more work")
	}
	short, end := context.WithTimeout(ctx, 5*time.Millisecond)
	if owner.AwaitWorkload(short) == nil {
		t.Fatal("revocation manufactured workload cleanup")
	}
	end()
	close(release)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("revoked launch published authority")
		}
	case <-ctx.Done():
		t.Fatal("launch owner did not join")
	}
	gate.Lock()
	if err := writer.ConfirmWorkloadLocked(); err != nil {
		t.Error(err)
	}
	gate.Unlock()
	if err := owner.AwaitWorkload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Prepare(policy); !errors.Is(err, ephemeral.ErrDenied) {
		t.Fatal("revoked issuer created a new grant", err)
	}
}

type delayedDeadline struct {
	context.Context
	deadline time.Time
}

func (c delayedDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestOwnerWorkloadWaitHonorsActualDeadlineWithoutTimerDelivery(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	var gate sync.Mutex
	writer, owner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	ctx := delayedDeadline{Context: context.Background(), deadline: time.Now().Add(10 * time.Millisecond)}
	result := make(chan error, 1)
	go func() { result <- owner.AwaitWorkload(ctx) }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("deadline became cleanup confirmation")
		}
	case <-time.After(100 * time.Millisecond):
		// Release the memory waiter even on the red implementation.
		gate.Lock()
		writer.NotifyPendingLocked()
		gate.Unlock()
		<-result
		t.Fatal("workload waiter exceeded its actual finite deadline")
	}
}

func TestOwnerAdmissionAndWorkloadWaitRespectDeadlineWhileGateContended(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	var gate sync.Mutex
	_, owner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		name string
		call func(context.Context) error
	}{
		{"admit", owner.Admit}, {"await-workload", owner.AwaitWorkload},
	} {
		t.Run(operation.name, func(t *testing.T) {
			gate.Lock()
			ctx := delayedDeadline{Context: context.Background(), deadline: time.Now().Add(10 * time.Millisecond)}
			result := make(chan error, 1)
			go func() { result <- operation.call(ctx) }()
			select {
			case err := <-result:
				gate.Unlock()
				if err == nil {
					t.Fatal("contended gate granted operation")
				}
			case <-time.After(100 * time.Millisecond):
				gate.Unlock()
				<-result
				t.Fatal("gate acquisition exceeded the actual finite deadline")
			}
		})
	}
}

func TestOwnerAdmissionRefusesBusyIssuerWithoutHoldingServiceGatePastDeadline(t *testing.T) {
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	var gate sync.Mutex
	_, owner, err := serviceauthowner.New(issuer, &gate)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	policy := ephemeral.ApprovedPolicy{PolicyDigest: strings.Repeat("a", 64), KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: now.Add(20 * time.Second), BackendLeaseCeiling: 3 * time.Second}
	challenge, err := issuer.Prepare(policy)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enter sync.Once
	launched := make(chan error, 1)
	go func() {
		_, err := issuer.Launch(ctx, challenge, func() time.Time {
			enter.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
			}
			return now
		}, func(context.Context, *supervisor.Authority) error { return nil })
		launched <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("issuer clock did not enter memory barrier")
	}
	short, end := context.WithTimeout(ctx, 10*time.Millisecond)
	defer end()
	result := make(chan error, 1)
	go func() { result <- owner.Admit(short) }()
	bounded := true
	var admitErr error
	select {
	case admitErr = <-result:
	case <-time.After(100 * time.Millisecond):
		bounded = false
	}
	close(release)
	if !bounded {
		admitErr = <-result
	}
	select {
	case <-launched:
	case <-ctx.Done():
		t.Fatal("issuer memory worker did not join")
	}
	_ = owner.Revoke()
	if !bounded {
		t.Fatal("issuer observation held service gate beyond admission deadline", admitErr)
	}
	if admitErr == nil {
		t.Fatal("busy issuer admitted a new acquisition")
	}
}
