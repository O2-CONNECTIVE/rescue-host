package childjob

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrepareRejectsInvalidInputBeforePlatformAccess(t *testing.T) {
	if p, err := Prepare(nil, Spec{}); p != nil || !errors.Is(err, ErrConfig) {
		t.Fatalf("invalid prepare reached platform: %v", err)
	}
}

func TestPrepareDoesNotExecuteBeforeInspectionAndCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil || p == nil {
		t.Fatalf("prepare: %v", err)
	}
	defer p.Close(ctx)
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("target executed during Prepare, before inspection or Commit")
		}
	}
	if err := p.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	inspection, resume := -1, -1
	for i, call := range api.calls {
		if call == "inspect-target" {
			inspection = i
		}
		if call == "resume" {
			if resume != -1 {
				t.Fatal("resumed twice")
			}
			resume = i
		}
	}
	if inspection < 0 || resume <= inspection {
		t.Fatal("inspection did not precede execution")
	}
}

func TestInspectionFailureRetainsAndClosesReturnedQueryHandle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{failure: "inspect-target"}}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(ctx); !errors.Is(err, ErrStart) {
		t.Fatalf("inspection failure: %v", err)
	}
	closed := 0
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("failed inspection executed")
		}
		if call == "close-query" {
			closed++
		}
	}
	if closed != 1 {
		t.Fatalf("partial query handle lost: closed=%d", closed)
	}
}

func TestCanceledAdmissionCannotBeReplacedByFreshCallContext(t *testing.T) {
	lease, revoke := context.WithTimeout(context.Background(), time.Second)
	defer revoke()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, err := prepareWithKernel(lease, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	revoke()
	fresh, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Commit(fresh); !errors.Is(err, ErrStart) {
		t.Fatalf("old admission restored: %v", err)
	}
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("canceled admission executed")
		}
	}
	if err := p.Close(fresh); err != nil {
		t.Fatal(err)
	}
}

func TestQueryCloseFailureRemainsOwnedForRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	api.failure = "close-query"
	if err := p.Close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("unconfirmed query discarded: %v", err)
	}
	if p.queryToken != 5 {
		t.Fatal("lost query handle after close failure")
	}
	api.failure = ""
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if p.queryToken != 0 {
		t.Fatal("query handle not released")
	}
	if err := p.Commit(ctx); !errors.Is(err, ErrStart) {
		t.Fatal("closed target replayed")
	}
}

func TestRevocationDuringInspectionCannotResumeOrReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	api := &inspectionMemory{memoryKernel: &memoryKernel{onCall: func(name string) {
		if name == "inspect-target" {
			close(entered)
			<-release
		}
	}}}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- p.Commit(ctx) }()
	<-entered
	if err := p.Revoke(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrStart) {
		t.Fatalf("revoked commit accepted: %v", err)
	}
	fresh, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if err := p.Commit(fresh); !errors.Is(err, ErrStart) {
		t.Fatalf("old Prepared replay accepted: %v", err)
	}
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("revocation did not stop resume")
		}
	}
	if err := p.Close(fresh); err != nil {
		t.Fatal(err)
	}
}

type inspectionMemory struct{ *memoryKernel }

func (m *inspectionMemory) close(handle uintptr) error {
	if handle == 5 {
		return m.record("close-query")
	}
	return m.memoryKernel.close(handle)
}
func (m *inspectionMemory) inspectTarget(_ Spec, process, thread, job uintptr) (uintptr, error) {
	if process != 3 || thread != 4 || job != 2 {
		panic("different target")
	}
	return 5, m.record("inspect-target")
}

func TestRevocationDuringResumeReturnIsReclaimedNotReportedReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	api.onCall = func(name string) {
		if name == "resume" {
			_ = p.Revoke()
		}
	}
	if err := p.Commit(ctx); !errors.Is(err, ErrStart) {
		t.Fatalf("revoked late result reported ready: %v", err)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if p.child.process != 0 || p.child.job != 0 {
		t.Fatal("late result lost cleanup ownership")
	}
}
