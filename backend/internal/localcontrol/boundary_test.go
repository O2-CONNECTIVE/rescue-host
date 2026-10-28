package localcontrol

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
)

type fakeKernel struct {
	config                                    Config
	events                                    []string
	thread                                    bool
	peer                                      peerToken
	impersonateError, revertError, tokenError error
	fatalCount                                int
	plan                                      PipePlan
	command                                   Command
	tokenEntered, tokenRelease                chan struct{}
}

func (f *fakeKernel) create(plan PipePlan) (uintptr, error) {
	f.plan = plan
	f.events = append(f.events, "create")
	return 123, nil
}
func (f *fakeKernel) close(uintptr) error   { f.events = append(f.events, "close"); return nil }
func (f *fakeKernel) inspect(uintptr) error { f.events = append(f.events, "inspect"); return nil }
func (f *fakeKernel) read(context.Context, uintptr) (Command, error) {
	f.events = append(f.events, "read")
	return f.command, nil
}
func (f *fakeKernel) serverSID() (string, error) { return f.config.ServiceSID, nil }
func (f *fakeKernel) pin()                       { f.events = append(f.events, "pin") }
func (f *fakeKernel) unpin()                     { f.events = append(f.events, "unpin") }
func (f *fakeKernel) hasThreadToken() (bool, error) {
	f.events = append(f.events, "thread-check")
	return f.thread, nil
}
func (f *fakeKernel) impersonate(uintptr) error {
	f.events = append(f.events, "impersonate")
	return f.impersonateError
}
func (f *fakeKernel) clientToken() (peerToken, error) {
	f.events = append(f.events, "token")
	if f.tokenEntered != nil {
		close(f.tokenEntered)
		<-f.tokenRelease
	}
	return f.peer, f.tokenError
}
func (f *fakeKernel) revert() error { f.events = append(f.events, "revert"); return f.revertError }
func (f *fakeKernel) fatal()        { f.fatalCount++; f.events = append(f.events, "fatal") }

func execute(e *Endpoint, ctx context.Context, command Command) (backend.Snapshot, error) {
	e.boundary.api.(*fakeKernel).command = command
	return e.Next(ctx)
}

type fakeDispatcher struct {
	calls int
	api   *fakeKernel
	err   error
}

func (d *fakeDispatcher) Dispatch(ctx context.Context, op control.Operation, id backend.Identity) (backend.Snapshot, error) {
	d.calls++
	d.api.events = append(d.api.events, "dispatch")
	return backend.Snapshot{Identity: id, State: "terminated"}, d.err
}

func TestVerifiedOperatorCanStopOnlyBoundSession(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}}
	dispatch := &fakeDispatcher{api: api}
	b, err := newBoundary(config, api, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := b.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop})
	if err != nil || snapshot.Identity != config.Identity || dispatch.calls != 1 {
		t.Fatal("kernel-verified operator stop rejected")
	}
}

func TestAuthorizedStopFailureRemainsUnconfirmed(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}}
	dispatch := &fakeDispatcher{api: api, err: errors.New("SECRET_TEST_ERROR")}
	b, _ := newBoundary(config, api, dispatch)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	if _, err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop}); err != ErrUnconfirmed || dispatch.calls != 1 {
		t.Fatal("authorized stop failure lost its unconfirmed status")
	}
}

func TestPeerAndImpersonationFailuresNeverDispatch(t *testing.T) {
	for _, name := range []string{"guest", "unlisted", "primary", "anonymous", "impersonate", "token-query", "revert", "preexisting-thread", "token-cleanup"} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}}
			dispatch := &fakeDispatcher{api: api}
			switch name {
			case "guest":
				api.peer.sid = config.GuestSID
			case "unlisted":
				api.peer.sid = "S-1-5-21-101-102-103-2000"
			case "primary":
				api.peer.tokenType = 1
			case "anonymous":
				api.peer.level = 0
			case "impersonate":
				api.impersonateError = errors.New("SECRET_TEST_IMPERSONATE")
			case "token-query":
				api.tokenError = errors.New("SECRET_TEST_TOKEN")
			case "revert":
				api.revertError = errors.New("SECRET_TEST_REVERT")
			case "preexisting-thread":
			case "token-cleanup":
				api.tokenError = ErrFatal
			}
			b, _ := newBoundary(config, api, dispatch)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e, _ := b.open(ctx)
			api.events = nil // ここから要求の認証scopeだけを観測する。
			if name == "preexisting-thread" {
				api.thread = true
			}
			_, err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop})
			if err != ErrDenied && err != ErrFatal {
				t.Fatal("native error escaped fixed result")
			}
			if dispatch.calls != 0 {
				t.Fatal("unauthorized privileged request executed")
			}
			for _, event := range api.events {
				if name == "impersonate" && (event == "token" || event == "revert") {
					t.Fatal("impersonation failure fell through")
				}
				if (name == "revert" || name == "preexisting-thread") && event == "unpin" {
					t.Fatal("unsafe thread returned to pool")
				}
			}
			if name == "revert" || name == "preexisting-thread" || name == "token-cleanup" {
				if api.fatalCount != 1 {
					t.Fatal("unsafe state was not fatal")
				}
				if _, err = execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop}); err != ErrFatal {
					t.Fatal("poisoned endpoint resumed")
				}
			}
		})
	}
}

func TestKernelRestorationPrecedesPrivilegedDispatch(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, peer: peerToken{sid: config.ServiceSID, tokenType: 2, level: 2}}
	dispatch := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, dispatch)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	if _, err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.ReadSnapshot}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.events, []string{"pin", "thread-check", "create", "unpin", "read", "inspect", "pin", "thread-check", "impersonate", "token", "revert", "unpin", "dispatch"}) {
		t.Fatal("Windows API restoration order changed")
	}
}

func TestGenerationIdentityAndStartCannotEscapeScope(t *testing.T) {
	for _, name := range []string{"generation", "identity", "start"} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}}
			dispatch := &fakeDispatcher{api: api}
			b, _ := newBoundary(config, api, dispatch)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e, _ := b.open(ctx)
			command := Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop}
			switch name {
			case "generation":
				command.Generation++
			case "identity":
				command.Identity.Epoch = "2"
			case "start":
				command.Operation = control.Start
			}
			if _, err := execute(e, ctx, command); err != ErrDenied || dispatch.calls != 0 {
				t.Fatal("scope escape accepted")
			}
		})
	}
}

func TestPublicRuntimeGateNeverCreatesPipe(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config}
	dispatch := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, dispatch)
	if _, err := b.Open(context.Background()); err != ErrClosed || len(api.events) != 0 {
		t.Fatal("runtime gate created a pipe")
	}
}

func TestFactoryRejectsAmbientThreadTokenBeforeCreate(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config, thread: true}
	dispatch := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, dispatch)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := b.open(ctx); err != ErrFatal || api.fatalCount != 1 {
		t.Fatal("factory accepted ambient client context")
	}
	for _, event := range api.events {
		if event == "create" || event == "unpin" {
			t.Fatal("factory created pipe or released unsafe thread")
		}
	}
}

func TestCloseCannotInterleaveClientTokenScope(t *testing.T) {
	config := testConfig()
	release := make(chan struct{})
	entered := make(chan struct{})
	api := &fakeKernel{config: config, peer: peerToken{sid: config.OperatorSID, tokenType: 2, level: 1}, tokenEntered: entered, tokenRelease: release}
	dispatch := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, dispatch)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, _ := b.open(ctx)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		_, err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("token boundary not reached")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer closeCancel()
	if err := e.Close(closeCtx); err != ErrDenied {
		t.Fatal("close entered active token scope")
	}
	if dispatch.calls != 0 {
		t.Fatal("dispatch ran before revert")
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("token scope did not finish")
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Next(ctx); err != ErrClosed {
		t.Fatal("closed endpoint resumed")
	}
}

func TestServerIdentityNeverFallsBackToLocalSystem(t *testing.T) {
	config := testConfig()
	api := &fakeKernel{config: config}
	dispatch := &fakeDispatcher{api: api}
	b, _ := newBoundary(config, api, dispatch)
	api.config.ServiceSID = "S-1-5-18"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := b.open(ctx); err != ErrDenied || len(api.events) != 0 {
		t.Fatal("unexpected server identity created pipe")
	}
}
