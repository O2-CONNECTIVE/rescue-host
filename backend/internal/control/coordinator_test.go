package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
)

// 純粋状態のseam用。鍵・listener・SSH・子processは生成しない。
type memorySession struct {
	snapshot   backend.Snapshot
	closed     bool
	closeError error
	incomplete bool
}

func (s *memorySession) Snapshot() backend.Snapshot       { return s.snapshot }
func (s *memorySession) Revoke(id backend.Identity) error { s.snapshot.State = "revoking"; return nil }
func (s *memorySession) Close(context.Context) error {
	s.closed = true
	if s.closeError != nil {
		return s.closeError
	}
	if !s.incomplete {
		s.snapshot.State = "terminated"
	}
	return nil
}

func TestStopConfirmsSameSession(t *testing.T) {
	id := backend.Identity{LaunchID: "control-test", Epoch: "control-epoch", PolicyDigest: strings.Repeat("a", 64)}
	s := &memorySession{snapshot: backend.Snapshot{Identity: id, State: "active"}}
	c, err := newCoordinator(s)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot, err := c.Stop(ctx, id)
	if err != nil || snapshot.State != "terminated" || snapshot.Identity != id || !s.closed {
		t.Fatal("same session close not confirmed")
	}
}

func TestStopRejectsOtherIdentity(t *testing.T) {
	id := backend.Identity{LaunchID: "control-test", Epoch: "control-epoch", PolicyDigest: strings.Repeat("a", 64)}
	s := &memorySession{snapshot: backend.Snapshot{Identity: id, State: "active"}}
	c, _ := newCoordinator(s)
	other := id
	other.Epoch = "old-epoch"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Stop(ctx, other); err == nil || s.closed || s.snapshot.State != "active" {
		t.Fatal("other identity changed owned session")
	}
}

func TestStopDoesNotClaimUnconfirmedEnd(t *testing.T) {
	for _, failure := range []string{"error", "incomplete", "no-budget"} {
		t.Run(failure, func(t *testing.T) {
			id := backend.Identity{LaunchID: "control-test", Epoch: "control-epoch", PolicyDigest: strings.Repeat("a", 64)}
			s := &memorySession{snapshot: backend.Snapshot{Identity: id, State: "active"}}
			if failure == "error" {
				s.closeError = errors.New("SECRET_TEST_ERROR")
			}
			if failure == "incomplete" {
				s.incomplete = true
			}
			c, _ := newCoordinator(s)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if failure == "no-budget" {
				ctx = context.Background()
			}
			snapshot, err := c.Stop(ctx, id)
			if err == nil || snapshot.State == "terminated" || strings.Contains(err.Error(), "SECRET_TEST") {
				t.Fatal("unconfirmed end was successful or exposed raw error")
			}
			if failure == "no-budget" && (s.closed || s.snapshot.State != "active") {
				t.Fatal("unbounded stop changed session")
			}
		})
	}
}

func TestSnapshotPreservesAuthorityAndStartStaysClosed(t *testing.T) {
	id := backend.Identity{LaunchID: "control-test", Epoch: "control-epoch", PolicyDigest: strings.Repeat("a", 64)}
	s := &memorySession{snapshot: backend.Snapshot{Identity: id, State: "revoking", Reason: "explicit-stop", Owned: 1}}
	c, _ := newCoordinator(s)
	if err := c.Start(context.Background(), id); err == nil {
		t.Fatal("runtime activation was accepted")
	}
	snapshot, err := c.Snapshot(id)
	if err != nil || snapshot != s.snapshot || s.closed {
		t.Fatal("snapshot changed or fabricated authority state")
	}
	s.snapshot.Identity.Epoch = "different-epoch"
	if _, err = c.Snapshot(id); err == nil {
		t.Fatal("identity drift was accepted")
	}
}

func TestConstructorRejectsMissingAuthority(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil Session accepted")
	}
	if _, err := newCoordinator(&memorySession{}); err == nil {
		t.Fatal("invalid identity accepted")
	}
}

func TestDispatcherUsesSessionAuthority(t *testing.T) {
	id := backend.Identity{LaunchID: "control-test", Epoch: "control-epoch", PolicyDigest: strings.Repeat("a", 64)}
	s := &memorySession{snapshot: backend.Snapshot{Identity: id, State: "active"}}
	c, _ := newCoordinator(s)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Dispatch(ctx, Start, id); err == nil || s.closed {
		t.Fatal("start gate opened")
	}
	snapshot, err := c.Dispatch(ctx, ReadSnapshot, id)
	if err != nil || snapshot.State != "active" {
		t.Fatal("snapshot not dispatched")
	}
	if _, err := c.Dispatch(ctx, Operation(99), id); err == nil || s.closed {
		t.Fatal("unknown operation accepted")
	}
	snapshot, err = c.Dispatch(ctx, Stop, id)
	if err != nil || snapshot.State != "terminated" || !s.closed {
		t.Fatal("stop not dispatched")
	}
}
