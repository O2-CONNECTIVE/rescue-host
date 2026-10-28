package servicehost

import (
	"context"
	"errors"
	"fmt"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

type drainPipeFixture struct {
	stderr io.ReadCloser
	close  func(context.Context) error
}

func (*drainPipeFixture) ChildHandles() childjob.Handles {
	return childjob.Handles{Stdin: 10, Stdout: 11, Stderr: 12}
}
func (*drainPipeFixture) Input() io.WriteCloser              { return &nativeInput{} }
func (*drainPipeFixture) Output() io.ReadCloser              { return io.NopCloser(strings.NewReader("")) }
func (p *drainPipeFixture) Stderr() io.ReadCloser            { return p.stderr }
func (*drainPipeFixture) ReleaseChild(context.Context) error { return nil }
func (p *drainPipeFixture) Close(ctx context.Context) error {
	if p.close != nil {
		return p.close(ctx)
	}
	return p.stderr.Close()
}

func TestOwnedStdioDiscardsBoundedStderrAndJoinsRealEOF(t *testing.T) {
	p, err := newOwnedStdio(&drainPipeFixture{stderr: io.NopCloser(strings.NewReader("synthetic stderr"))}, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("bounded stderr EOF not confirmed: %v", err)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatalf("stdio cleanup failed: %v", err)
	}
}

func TestRejectedChildPlanRetainsAndClosesPartialStdio(t *testing.T) {
	_, grant := nativeConfigFixture(t)
	authority, err := supervisor.New(grant, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Revoke(grant.Binding)
	reader, writer := io.Pipe()
	defer writer.Close()
	closed := false
	stream, err := newOwnedStdio(&drainPipeFixture{stderr: reader, close: func(context.Context) error { closed = true; return reader.Close() }}, 16)
	if err != nil {
		t.Fatal(err)
	}
	source := nativeBaseFunc(func(context.Context, supervisor.ProvisionedGrant) (ChildPlan, error) {
		return ChildPlan{Stdio: stream}, errors.New("synthetic partial allocation")
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	process, err := NewJobRuntime(source).Start(ctx, authority)
	if process == nil || !errors.Is(err, ErrProvisioningRejected) {
		t.Fatalf("partial stdio ownership lost: process=%v error=%v", process, err)
	}
	if err := process.Close(ctx); err != nil || !closed {
		t.Fatalf("partial stdio not closed/joined: %v, closed=%v", err, closed)
	}
}

func TestOwnedStdioCannotMoveBetweenAuthorityGrants(t *testing.T) {
	_, g := nativeConfigFixture(t)
	first, err := supervisor.New(g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other := g
	other.Binding.Identity.LaunchID = "different_grant"
	second, err := supervisor.New(other, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Revoke(g.Binding)
	defer second.Revoke(other.Binding)
	owner, err := newOwnedStdio(&drainPipeFixture{stderr: io.NopCloser(strings.NewReader(""))}, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer owner.Close(ctx)
	owner.attach(first, g.Binding)
	owner.attach(second, other.Binding)
	if first.Snapshot(time.Now()).State != supervisor.Revoked || second.Snapshot(time.Now()).State != supervisor.Revoked {
		t.Fatal("stdio ownership rebound without irrevocable rejection")
	}
}

func TestStdioCloseRevokesEvenWhenCleanupBudgetIsMissing(t *testing.T) {
	_, g := nativeConfigFixture(t)
	authority, err := supervisor.New(g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Revoke(g.Binding)
	owner, err := newOwnedStdio(&drainPipeFixture{stderr: io.NopCloser(strings.NewReader(""))}, 16)
	if err != nil {
		t.Fatal(err)
	}
	owner.attach(authority, g.Binding)
	if err := owner.Close(context.Background()); !errors.Is(err, ErrRuntimeUnconfirmed) {
		t.Fatal("unbounded cleanup accepted")
	}
	if authority.Snapshot(time.Now()).State != supervisor.Revoked {
		t.Fatal("missing cleanup budget retained grant")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = owner.Close(ctx)
}

func TestStderrLimitFailureRevokesBeforeAndAfterAuthorityAttachment(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			_, g := nativeConfigFixture(t)
			a, err := supervisor.New(g, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer a.Revoke(g.Binding)
			owner, err := newOwnedStdio(&drainPipeFixture{stderr: io.NopCloser(strings.NewReader(strings.Repeat("x", 17)))}, 16)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			defer owner.Close(ctx)
			if !before {
				owner.attach(a, g.Binding)
			}
			if err := owner.Wait(ctx); !errors.Is(err, ErrRuntimeUnconfirmed) || ctx.Err() != nil {
				t.Fatalf("overflow was accepted or not observed: %v", err)
			}
			if before {
				owner.attach(a, g.Binding)
			}
			if a.Snapshot(time.Now()).State != supervisor.Revoked {
				t.Fatal("stderr overflow retained grant")
			}
		})
	}
}

type stderrDiagnosticReader struct{}

func (stderrDiagnosticReader) Read([]byte) (int, error) {
	return 0, errors.New("SYNTHETIC_PRIVATE_DIAGNOSTIC")
}
func (stderrDiagnosticReader) Close() error { return nil }
func TestStderrDiagnosticIsNotForwarded(t *testing.T) {
	owner, err := newOwnedStdio(&drainPipeFixture{stderr: stderrDiagnosticReader{}}, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer owner.Close(ctx)
	err = owner.Wait(ctx)
	if !errors.Is(err, ErrRuntimeUnconfirmed) || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE") {
		t.Fatalf("stderr failure forwarded diagnostics: %v", err)
	}
}

type drainBlockedReader struct {
	entered, release chan struct{}
	enter, close     sync.Once
}

func (p *drainBlockedReader) Read([]byte) (int, error) {
	p.enter.Do(func() { close(p.entered) })
	<-p.release
	return 0, io.ErrClosedPipe
}
func (p *drainBlockedReader) Close() error { p.close.Do(func() { close(p.release) }); return nil }
func TestFailedStdioCloseRetainsDrainAndAllowsSameOwnerCleanup(t *testing.T) {
	reader := &drainBlockedReader{entered: make(chan struct{}), release: make(chan struct{})}
	defer reader.Close()
	attempts := 0
	pipes := &drainPipeFixture{stderr: reader, close: func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("synthetic close unconfirmed")
		}
		return reader.Close()
	}}
	owner, err := newOwnedStdio(pipes, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-reader.entered:
	case <-ctx.Done():
		t.Fatal("drain did not start")
	}
	if err := owner.Close(ctx); !errors.Is(err, ErrRuntimeUnconfirmed) {
		t.Fatal("failed close was confirmed")
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatalf("same owner retry failed: %v", err)
	}
	if err := owner.Wait(ctx); !errors.Is(err, ErrRuntimeUnconfirmed) {
		t.Fatal("self-close became real stderr EOF")
	}
}

func TestClosedStdioCannotAcquireANewGrant(t *testing.T) {
	_, g := nativeConfigFixture(t)
	a, err := supervisor.New(g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Revoke(g.Binding)
	owner, err := newOwnedStdio(&drainPipeFixture{stderr: io.NopCloser(strings.NewReader(""))}, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	owner.attach(a, g.Binding)
	if a.Snapshot(time.Now()).State != supervisor.Revoked {
		t.Fatal("closed stdio revived an authority")
	}
}
