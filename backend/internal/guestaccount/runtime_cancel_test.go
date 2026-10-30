package guestaccount

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeGuestCancellationRetainsSecretAndLateTokenUntilActualReturn(t *testing.T) {
	r, f := runtimeOwnerFixture(t)
	f.token.entered, f.token.release = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() { _, err := r.Prepare(ctx); done <- err }()
	<-f.token.entered
	cancel()
	if err := r.Close(testContext(t)); !errors.Is(err, ErrBusy) {
		t.Fatal("pending logon acknowledged closure", err)
	}
	if f.token.closed || f.secret[0] == 0 {
		t.Fatal("native call lost its token owner or credential buffer")
	}
	close(f.token.release)
	if err := <-done; err == nil {
		t.Fatal("late logon succeeded after cancellation")
	}
	for _, event := range f.trace {
		if event == "prepare-target" {
			t.Fatal("late token created workload")
		}
	}
	if err := r.Close(testContext(t)); err != nil {
		t.Fatal("late token not reclaimed", err)
	}
	if !f.token.closed {
		t.Fatal("late token lost")
	}
	for _, c := range f.secret {
		if c != 0 {
			t.Fatal("credential retained after actual closure")
		}
	}
}
