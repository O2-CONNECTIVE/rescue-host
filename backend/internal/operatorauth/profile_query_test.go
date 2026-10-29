package operatorauth

import (
	"context"
	"testing"
	"time"
)

type profileQueryKernelFixture struct {
	state         profileCandidateSnapshot
	closeErr      error
	closes        int
	onCurrent     func()
	probeEvidence *profilePrimaryEvidence
	currentErr    error
}

func (f *profileQueryKernelFixture) subject() error { return nil }
func (f *profileQueryKernelFixture) process(uintptr) (profileCandidateSnapshot, error) {
	return f.state, nil
}
func (f *profileQueryKernelFixture) token(h uintptr) (profilePrimaryEvidence, error) {
	if h == 77 && f.probeEvidence != nil {
		return *f.probeEvidence, nil
	}
	return profilePrimaryEvidence{f.state.primary, f.state.tokenID, f.state.modifiedID}, nil
}
func (f *profileQueryKernelFixture) current(uintptr) (uintptr, error) {
	if f.onCurrent != nil {
		f.onCurrent()
	}
	return 77, f.currentErr
}
func (f *profileQueryKernelFixture) close(uintptr) error { f.closes++; return f.closeErr }

func TestProfileQueryRetainsProbeAfterCloseFailure(t *testing.T) {
	_, q := providerFixture()
	k := &profileQueryKernelFixture{state: q.snapshot, closeErr: ErrUnconfirmed}
	x, err := newProfileQuery(11, 12, k)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = x.inspect(ctx); err == nil {
		t.Fatal("probe close failure accepted")
	}
	if x.probe != 77 {
		t.Fatal("probe owner lost")
	}
	k.closeErr = nil
	if err = x.close(ctx); err != nil || x.probe != 0 {
		t.Fatalf("probe recovery: %v", err)
	}
	if _, err = x.inspect(ctx); err == nil {
		t.Fatal("closed query revived")
	}
}

func TestProfileQueryRejectsCurrentProcessTokenReplacementAndRetainsPartial(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "partial"}[partial], func(t *testing.T) {
			_, q := providerFixture()
			k := &profileQueryKernelFixture{state: q.snapshot}
			other := profilePrimaryEvidence{projection: q.snapshot.primary, tokenID: 99, modifiedID: 9}
			if partial {
				k.currentErr = ErrDenied
			} else {
				k.probeEvidence = &other
			}
			x, err := newProfileQuery(11, 12, k)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err = x.inspect(ctx); err == nil || x.probe != 77 {
				t.Fatalf("invalid/current partial lost: %v", err)
			}
			if err = x.close(ctx); err != nil || k.closes != 1 {
				t.Fatalf("retained probe cleanup: %v closes=%d", err, k.closes)
			}
		})
	}
}

func TestProfileQueryLateCancellationRetainsReturnedProbeUntilClose(t *testing.T) {
	_, q := providerFixture()
	k := &profileQueryKernelFixture{state: q.snapshot}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k.onCurrent = cancel
	x, err := newProfileQuery(11, 12, k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.inspect(ctx); err == nil {
		t.Fatal("cancelled query accepted")
	}
	cleanup, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = x.close(cleanup); err != nil {
		t.Fatal(err)
	}
	if k.closes != 1 {
		t.Fatalf("probe closed %d times", k.closes)
	}
}

func TestProfileQueryConstructorPanicRetainsUnknownAcquisition(t *testing.T) {
	_, q := providerFixture()
	k := &profileQueryKernelFixture{state: q.snapshot, onCurrent: func() { panic("synthetic acquisition") }}
	x, err := newProfileQuery(11, 12, k)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = x.inspect(ctx); err == nil {
		t.Fatal("panic accepted")
	}
	if err = x.close(ctx); err == nil {
		t.Fatal("unknown acquisition called complete")
	}
}
