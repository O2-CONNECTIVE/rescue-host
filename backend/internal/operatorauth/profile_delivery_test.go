package operatorauth

import (
	"context"
	"sync"
	"testing"
)

type deliveryChild struct {
	*memoryBatchChild
	onResume func()
}

func (c *deliveryChild) resume(ctx context.Context) error {
	if err := c.memoryBatchChild.resume(ctx); err != nil {
		return err
	}
	c.onResume()
	return nil
}

// This is a connected memory-kernel path, not a Windows pipe/provenance test.
func TestProfileDeliveryBatchToBoundHelperEntry(t *testing.T) {
	ctx, receiver, _ := receiverSetup(t)
	data := make(chan []byte, 1)
	var once sync.Once
	api := &profileSyncFixture{transferFn: func(kind profileSyncKind, _ uintptr, b []byte) (uint32, error) {
		if kind == profileSyncWrite {
			data <- append([]byte(nil), b...)
			return uint32(len(b)), nil
		}
		chunk, ok := <-data
		if !ok {
			return 0, errProfileSyncEOF
		}
		return uint32(copy(b, chunk)), nil
	}}
	receiver.ops.start = func(c context.Context, h uintptr, b []byte) (*profileSyncOwner, error) {
		return startProfileSync(c, api, profileSyncRead, h, b)
	}
	kernel := &profilePipeKernelFixture{inheritFn: func(uintptr) (uintptr, error) { return 77, nil }, closeFn: func(h uintptr) error {
		if h == 52 {
			once.Do(func() { close(data) })
		}
		return nil
	}}
	s, owner, ops, _, _, child := batchFixture(t)
	ops.newProfile = func(c context.Context) (batchProfile, error) {
		return newProfilePipe(c, profilePipeTestProducer(), kernel, api)
	}
	finished := make(chan error, 1)
	h, thread, _, published := helperFixture()
	material, _ := profilePipeTestProducer().prepare(ctx)
	thread.projection = material.profile.operator
	ops.start = func(_ context.Context, l nativeLaunchSpec) (batchChild, error) {
		if l.profileRead != 77 || l.launchID != material.launchID {
			return nil, ErrDenied
		}
		return &deliveryChild{child, func() { go func() { finished <- receiver.run(ctx, h.ops) }() }}, nil
	}
	b, err := startBatch(ctx, s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
	case <-ctx.Done():
		t.Fatal("profile delivery did not finish")
	}
	if err != nil || string(*published) != string(requestFrame()) {
		t.Fatal("bound helper entry failed", err)
	}
	proof, err := b.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = proof.Consume(ctx); err != nil {
		t.Fatal(err)
	}
}
