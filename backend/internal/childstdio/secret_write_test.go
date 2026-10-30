package childstdio

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type secretWriter interface{ WriteSecret([]byte) (int, error) }

type secretFakeOp struct {
	mu                      sync.Mutex
	buffer                  []byte
	done, released          bool
	releaseFailure          bool
	zeroBeforeRelease       bool
	panicPoll, panicRelease bool
}

func (o *secretFakeOp) poll() (bool, uint32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.panicPoll {
		panic("fixed diagnostic")
	}
	if o.done {
		return true, uint32(len(o.buffer)), nil
	}
	return false, 0, nil
}
func (o *secretFakeOp) cancel() error { return nil }
func (o *secretFakeOp) release() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.panicRelease {
		panic("fixed diagnostic")
	}
	if !o.done {
		return ErrUnconfirmed
	}
	o.zeroBeforeRelease = allZero(o.buffer)
	if o.releaseFailure {
		return ErrUnconfirmed
	}
	o.released = true
	return nil
}
func (o *secretFakeOp) finish() { o.mu.Lock(); o.done = true; o.mu.Unlock() }
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

type secretFakeKernel struct {
	*fakeKernel
	op            *secretFakeOp
	mode          string
	buffer        []byte
	started       chan struct{}
	continueStart <-chan struct{}
}

func (k *secretFakeKernel) start(kind ioKind, handle uintptr, b []byte) (operation, error) {
	if kind != writeIO {
		return k.fakeKernel.start(kind, handle, b)
	}
	k.buffer = b
	if k.started != nil {
		close(k.started)
	}
	if k.continueStart != nil {
		<-k.continueStart
	}
	switch k.mode {
	case "panic":
		panic("fixed diagnostic")
	case "none":
		return nil, ErrConfig
	case "invalid-nil":
		return nil, nil
	case "partial":
		k.op.buffer = b
		return k.op, ErrConfig
	default:
		k.op.buffer = b
		return k.op, nil
	}
}
func secretPipes(t *testing.T, k *secretFakeKernel) (*Pipes, secretWriter) {
	t.Helper()
	k.fakeKernel = &fakeKernel{}
	p, err := newPipes(deadline(t), setup(), k)
	if err != nil {
		t.Fatal("fake pipe setup failed")
	}
	w, ok := p.Input().(secretWriter)
	if !ok {
		t.Fatal("secret writer unavailable")
	}
	return p, w
}

func TestSecretWriteWipesOwnedCopyAfterReleaseOnly(t *testing.T) {
	op := &secretFakeOp{done: true}
	k := &secretFakeKernel{op: op}
	p, w := secretPipes(t, k)
	caller := []byte{11, 12, 13}
	n, err := w.WriteSecret(caller)
	if err != nil || n != len(caller) || allZero(caller) || !allZero(k.buffer) || !op.released || op.zeroBeforeRelease {
		t.Fatal("completion, caller ownership or wipe ordering failed")
	}
	if p.Close(deadline(t)) != nil {
		t.Fatal("completed close failed")
	}
}

func TestSecretWritePendingRetainsCopyUntilCloseJoins(t *testing.T) {
	op := &secretFakeOp{}
	k := &secretFakeKernel{op: op, started: make(chan struct{})}
	p, w := secretPipes(t, k)
	caller := []byte{21, 22, 23}
	result := make(chan error, 1)
	go func() { _, err := w.WriteSecret(caller); result <- err }()
	<-k.started
	if !errors.Is(p.Input().Close(), ErrUnconfirmed) {
		t.Fatal("pending close reported success")
	}
	if !errors.Is(<-result, io.ErrClosedPipe) {
		t.Fatal("pending write did not stop")
	}
	caller[0] = 31
	if allZero(k.buffer) || op.released {
		t.Fatal("pending buffer was lost or wiped")
	}
	op.finish()
	if p.Close(deadline(t)) != nil || !op.released || !allZero(k.buffer) || caller[0] != 31 {
		t.Fatal("same owner did not release and wipe")
	}
}

func TestSecretWriteRetainsOnReleaseFailureUntilRetry(t *testing.T) {
	op := &secretFakeOp{done: true, releaseFailure: true}
	k := &secretFakeKernel{op: op}
	p, w := secretPipes(t, k)
	if _, err := w.WriteSecret([]byte{41, 42}); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("release failure reported success")
	}
	if allZero(k.buffer) || op.released {
		t.Fatal("release failure wiped or discarded owner")
	}
	op.mu.Lock()
	op.releaseFailure = false
	op.mu.Unlock()
	if p.Close(deadline(t)) != nil || !op.released || !allZero(k.buffer) {
		t.Fatal("release retry did not wipe")
	}
}

func TestSecretWritePartialStartErrorRetainsUntilRelease(t *testing.T) {
	op := &secretFakeOp{}
	k := &secretFakeKernel{op: op, mode: "partial"}
	p, w := secretPipes(t, k)
	if _, err := w.WriteSecret([]byte{51, 52}); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("partial start reported success")
	}
	if allZero(k.buffer) || op.released {
		t.Fatal("partial start lost owner")
	}
	op.finish()
	if p.Close(deadline(t)) != nil || !allZero(k.buffer) || !op.released {
		t.Fatal("partial operation was not collected")
	}
}

func TestSecretWriteStartPanicRetainsUnknownOwnership(t *testing.T) {
	k := &secretFakeKernel{mode: "panic"}
	p, w := secretPipes(t, k)
	if _, err := w.WriteSecret([]byte{61, 62}); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unknown start reported success")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if !errors.Is(p.Close(ctx), ErrUnconfirmed) || allZero(k.buffer) {
		t.Fatal("unknown operation was freed or reported complete")
	}
}

func TestSecretWriteNilOperationWithoutErrorRetainsUnknownOwnership(t *testing.T) {
	k := &secretFakeKernel{mode: "invalid-nil"}
	p, w := secretPipes(t, k)
	if _, err := w.WriteSecret([]byte{65, 66}); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unknown start reported success")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if !errors.Is(p.Close(ctx), ErrUnconfirmed) || allZero(k.buffer) {
		t.Fatal("unknown operation was freed or reported complete")
	}
}

func TestSecretWriteOperationPanicRetainsSameOwner(t *testing.T) {
	for _, kind := range []string{"poll", "release"} {
		t.Run(kind, func(t *testing.T) {
			op := &secretFakeOp{done: true, panicPoll: kind == "poll", panicRelease: kind == "release"}
			k := &secretFakeKernel{op: op}
			p, w := secretPipes(t, k)
			if _, err := w.WriteSecret([]byte{63, 64}); !errors.Is(err, ErrUnconfirmed) {
				t.Fatal("operation panic reported success")
			}
			if allZero(k.buffer) || op.released {
				t.Fatal("operation panic lost owned copy")
			}
			op.mu.Lock()
			op.panicPoll, op.panicRelease = false, false
			op.mu.Unlock()
			if p.Close(deadline(t)) != nil || !op.released || !allZero(k.buffer) {
				t.Fatal("operation panic retry did not confirm release")
			}
		})
	}
}

func TestSecretWriteStartingCannotBeMistakenForCompletion(t *testing.T) {
	op := &secretFakeOp{}
	gate := make(chan struct{})
	k := &secretFakeKernel{op: op, started: make(chan struct{}), continueStart: gate}
	p, w := secretPipes(t, k)
	result := make(chan error, 1)
	go func() { _, err := w.WriteSecret([]byte{71, 72}); result <- err }()
	<-k.started
	if !errors.Is(p.Input().Close(), ErrUnconfirmed) || allZero(k.buffer) {
		t.Fatal("starting operation reported complete")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	if !errors.Is(p.Close(ctx), ErrUnconfirmed) || allZero(k.buffer) {
		cancel()
		t.Fatal("starting operation escaped owner cleanup")
	}
	cancel()
	close(gate)
	if !errors.Is(<-result, io.ErrClosedPipe) {
		t.Fatal("canceled start reported success")
	}
	if allZero(k.buffer) {
		t.Fatal("pending operation was wiped")
	}
	op.finish()
	if p.Close(deadline(t)) != nil || !allZero(k.buffer) {
		t.Fatal("started operation was not collected")
	}
}

func TestSecretWriteBoundsAndNoOperationResult(t *testing.T) {
	k := &secretFakeKernel{mode: "none"}
	p, w := secretPipes(t, k)
	if n, err := w.WriteSecret(nil); err != nil || n != 0 || k.buffer != nil {
		t.Fatal("empty write started operation")
	}
	if _, err := w.WriteSecret(make([]byte, chunkBytes+1)); err == nil || k.buffer != nil {
		t.Fatal("oversized secret accepted")
	}
	caller := []byte{81, 82}
	if _, err := w.WriteSecret(caller); err == nil || !allZero(k.buffer) || allZero(caller) {
		t.Fatal("unstarted operation was not safely cleared")
	}
	if p.Close(deadline(t)) != nil {
		t.Fatal("unstarted operation blocked cleanup")
	}
}
