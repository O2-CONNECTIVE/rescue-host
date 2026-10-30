package childjob

import (
	"context"
	"testing"
	"time"
)

func TestInspectRetainsSameSuspendedTargetWithoutExecuting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, err := prepareWithKernel(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(ctx)
	observation, err := p.Inspect(ctx)
	if err != nil || observation == nil {
		t.Fatal("missing inspection", err)
	}
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("inspection executed before any issuer approval")
		}
	}
}

func TestInspectedTargetRejectsForeignReceiptAndSingleUseReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a := &inspectionMemory{memoryKernel: &memoryKernel{}}
	b := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, _ := prepareWithKernel(ctx, approvedSpec(), a)
	q, _ := prepareWithKernel(ctx, approvedSpec(), b)
	defer p.Close(ctx)
	defer q.Close(ctx)
	receipt, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if q.CommitInspected(ctx, receipt) == nil {
		t.Fatal("foreign target used the observation")
	}
	if p.Commit(ctx) == nil {
		t.Fatal("legacy combined path bypassed the inspected-only path")
	}
	if err := p.CommitInspected(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if p.CommitInspected(ctx, receipt) == nil {
		t.Fatal("same observation executed twice")
	}
	resumes := 0
	for _, call := range a.calls {
		if call == "resume" {
			resumes++
		}
	}
	if resumes != 1 {
		t.Fatal("not exactly one execution", resumes)
	}
	for _, call := range b.calls {
		if call == "resume" {
			t.Fatal("foreign target executed")
		}
	}
}

func TestInspectedTargetRejectsCancellationAndRevokedReceipt(t *testing.T) {
	for _, kind := range []string{"cancel", "revoke", "close", "copy"} {
		t.Run(kind, func(t *testing.T) {
			admission, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			fresh, done := context.WithTimeout(context.Background(), time.Second)
			defer done()
			api := &inspectionMemory{memoryKernel: &memoryKernel{}}
			p, _ := prepareWithKernel(admission, approvedSpec(), api)
			receipt, err := p.Inspect(admission)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "cancel":
				cancel()
			case "revoke":
				p.Revoke()
			case "close":
				p.Close(fresh)
			case "copy":
				copy := *receipt
				receipt = &copy
			}
			if p.CommitInspected(fresh, receipt) == nil {
				t.Fatal("dead or copied evidence resumed")
			}
			if err := p.Close(fresh); err != nil {
				t.Fatal(err)
			}
			for _, call := range api.calls {
				if call == "resume" {
					t.Fatal("invalid evidence executed")
				}
			}
		})
	}
}

func TestRevocationDuringSecondInspectionRejectsCommitAndRetainsCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, _ := prepareWithKernel(ctx, approvedSpec(), api)
	receipt, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	api.onCall = func(name string) {
		if name == "inspect-target" {
			close(entered)
			<-release
		}
	}
	result := make(chan error, 1)
	go func() { result <- p.CommitInspected(ctx, receipt) }()
	<-entered
	p.Revoke()
	close(release)
	if err := <-result; err == nil {
		t.Fatal("revoked second inspection committed")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, call := range api.calls {
		if call == "resume" {
			t.Fatal("revocation did not forbid resume")
		}
	}
}

func TestFailedSecondInspectionRetainsBothQueryReferencesUntilClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, _ := prepareWithKernel(ctx, approvedSpec(), api)
	receipt, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	api.failure = "close-query"
	api.onCall = func(name string) {
		if name == "inspect-target" {
			p.Revoke()
		}
	}
	if err := p.CommitInspected(ctx, receipt); err != ErrUnconfirmed {
		t.Fatal("uncertain cleanup was lost", err)
	}
	if p.queryToken == 0 || p.commitQueryToken == 0 {
		t.Fatal("one of the same-target query references was discarded")
	}
	api.failure = ""
	api.onCall = nil
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if p.queryToken != 0 || p.commitQueryToken != 0 {
		t.Fatal("query references not reclaimed")
	}
}

// Models an elapsed deadline whose cancellation notification has not arrived.
type inspectionDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c *inspectionDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func TestExpiredInspectionCannotBorrowFreshContextBeforeCancelNotification(t *testing.T) {
	admission := &inspectionDeadlineContext{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, err := prepareWithKernel(admission, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admission.deadline = time.Now().Add(-time.Second)
	if err := p.CommitInspected(ctx, receipt); err == nil {
		t.Error("expired inspection borrowed a fresh context")
	}
	_ = p.Close(ctx)
	for _, call := range api.calls {
		if call == "resume" {
			t.Error("expiry was postponed until notification")
		}
	}
}

func TestCallDeadlineDuringReinspectionCannotStartUnderLongerLease(t *testing.T) {
	lease, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	call := &inspectionDeadlineContext{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
	api := &inspectionMemory{memoryKernel: &memoryKernel{}}
	p, err := prepareWithKernel(lease, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Inspect(lease)
	if err != nil {
		t.Fatal(err)
	}
	api.onCall = func(name string) {
		if name == "inspect-target" {
			call.deadline = time.Now().Add(-time.Second)
		}
	}
	if err := p.CommitInspected(call, receipt); err == nil {
		t.Error("expired call borrowed the live preparation lease")
	}
	_ = p.Close(lease)
	for _, name := range api.calls {
		if name == "resume" {
			t.Error("expired reinspection resumed")
		}
	}
}
