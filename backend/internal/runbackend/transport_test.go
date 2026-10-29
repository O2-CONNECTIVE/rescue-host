package runbackend_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
)

type acceptedRuntimeSource struct {
	entered chan struct{}
	closing chan struct{}
	release <-chan struct{}
	once    sync.Once
	closed  atomic.Bool
}

func (s *acceptedRuntimeSource) Accept(ctx context.Context) (net.Conn, error) {
	s.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *acceptedRuntimeSource) Close(ctx context.Context) error {
	s.once.Do(func() { close(s.closing) })
	select {
	case <-s.release:
		s.closed.Store(true)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestAcceptedRuntimeSameSessionStopWaitsForSourceCloseBarrier(t *testing.T) {
	c := approved(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	source := &acceptedRuntimeSource{entered: make(chan struct{}, 1), closing: make(chan struct{}), release: release}
	c.AcceptedSource = source
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Coordinator().Start(ctx, c.Grant.Binding.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("same Runtime Session did not own/start accepted source")
	}
	stop, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
	snapshot, err := r.Coordinator().Stop(stop, c.Grant.Binding.Identity)
	end()
	if err == nil || snapshot.State == "terminated" {
		t.Fatal("Session stop terminated before source Close/join")
	}
	select {
	case <-source.closing:
	default:
		t.Fatal("stop did not request owned source Close")
	}
	releaseOnce.Do(func() { close(release) })
	final, finish := context.WithTimeout(context.Background(), time.Second)
	defer finish()
	if err := r.Close(final); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Identity != c.Grant.Binding.Identity || r.Snapshot().State != "terminated" {
		t.Fatal("source barrier changed Session or prevented confirmed close")
	}
}

type acceptedRuntimeOutput struct {
	bytes.Buffer
	closeObserved bool
	source        *acceptedRuntimeSource
}

func (o *acceptedRuntimeOutput) Close() error { o.closeObserved = o.source.closed.Load(); return nil }
func TestAcceptedRuntimeNormalEOFWaitsForSameSessionIngressBarrier(t *testing.T) {
	c := approved(t)
	release := make(chan struct{})
	close(release)
	source := &acceptedRuntimeSource{entered: make(chan struct{}, 1), closing: make(chan struct{}), release: release}
	c.AcceptedSource = source
	r, err := runbackend.New(c)
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.Join([][]byte{frame(t, c, "hello", 0, ""), frame(t, c, "start", 1, `{"policyRef":"approved-policy"}`)}, nil)
	out := &acceptedRuntimeOutput{source: source}
	if err := r.Run(context.Background(), io.NopCloser(bytes.NewReader(input)), out); err != nil {
		t.Fatal(err)
	}
	if !out.closeObserved {
		t.Fatal("normal stdout EOF preceded accepted source Close confirmation")
	}
	if !source.closed.Load() || r.Snapshot().State != "terminated" {
		t.Fatal("runtime failed sameSession cleanup barrier")
	}
}

func TestAcceptedRuntimeInvalidConfigurationLeavesSourceWithCaller(t *testing.T) {
	c := approved(t)
	c.Backend.Identity = backend.Identity{}
	release := make(chan struct{})
	close(release)
	source := &acceptedRuntimeSource{entered: make(chan struct{}, 1), closing: make(chan struct{}), release: release}
	c.AcceptedSource = source
	r, err := runbackend.New(c)
	if err == nil {
		t.Fatal("invalid runtime config accepted source")
	}
	if r != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = r.Close(ctx)
	}
	select {
	case <-source.entered:
		t.Fatal("failed constructor consumed source")
	case <-source.closing:
		t.Fatal("failed constructor closed caller-owned source")
	default:
	}
	if err := source.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
