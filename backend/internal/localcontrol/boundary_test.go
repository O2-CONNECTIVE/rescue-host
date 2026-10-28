package localcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
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
	frame, output                             []byte
	connects                                  int
	readEntered                               chan struct{}
	readError, writeError, connectError       error
	tokenEntered, tokenRelease                chan struct{}
}

func (f *fakeKernel) create(plan PipePlan) (uintptr, error) {
	f.plan = plan
	f.events = append(f.events, "create")
	return 123, nil
}
func (f *fakeKernel) close(uintptr) error                    { f.events = append(f.events, "close"); return nil }
func (f *fakeKernel) inspect(uintptr) error                  { f.events = append(f.events, "inspect"); return nil }
func (f *fakeKernel) connect(context.Context, uintptr) error { f.connects++; return f.connectError }
func (f *fakeKernel) read(ctx context.Context, _ uintptr) ([]byte, error) {
	f.events = append(f.events, "read")
	if f.readEntered != nil {
		close(f.readEntered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.frame, f.readError
}
func (f *fakeKernel) write(_ context.Context, _ uintptr, frame []byte) error {
	f.output = append([]byte(nil), frame...)
	return f.writeError
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

type Command struct {
	Generation uint64
	Identity   backend.Identity
	Operation  control.Operation
}

func execute(e *Endpoint, ctx context.Context, command Command) error {
	kind := "snapshot"
	payload := json.RawMessage(`{}`)
	if command.Operation == control.Stop {
		kind = "stop"
	}
	if command.Operation == control.Start {
		kind = "start"
		payload = json.RawMessage(`{"policyRef":"test"}`)
	}
	frame, err := control.Encode(control.Message{Protocol: control.Protocol, Kind: kind, Generation: strconv.FormatUint(command.Generation, 10), Sequence: "1", RequestID: "1", Identity: control.WireIdentity{LaunchID: command.Identity.LaunchID, Epoch: command.Identity.Epoch, PolicyDigest: command.Identity.PolicyDigest}, Payload: payload})
	if err != nil {
		return err
	}
	e.boundary.api.(*fakeKernel).frame = frame
	return e.Exchange(ctx)
}

type fakeDispatcher struct {
	calls           int
	closes          int
	closeError      error
	closeBudgetSeen bool
	api             *fakeKernel
	err             error
}

func (d *fakeDispatcher) Handle(_ context.Context, frame []byte) ([]byte, error) {
	d.calls++
	d.api.events = append(d.api.events, "dispatch")
	message, err := control.Decode(frame)
	if err != nil {
		return nil, err
	}
	if d.err != nil {
		return nil, d.err
	}
	if message.Kind == "hello" {
		message.Kind = "hello_ack"
	} else if message.Kind == "stop" {
		message.Kind = "receipt"
		message.Payload = json.RawMessage(`{"operation":"stop","accepted":true}`)
	} else {
		message.Kind = "snapshot_result"
		message.Payload = json.RawMessage(`{"goState":"terminated","pending":"0","active":"0","owned":"0","goCloseConfirmed":false}`)
	}
	return control.Encode(message)
}

func (d *fakeDispatcher) Close(ctx context.Context) error {
	d.closes++
	_, deadline := ctx.Deadline()
	d.closeBudgetSeen = deadline && ctx.Err() == nil
	return d.closeError
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
	err = execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop})
	if err != nil || dispatch.calls != 1 {
		t.Fatal("kernel-verified operator stop rejected")
	}
	response, err := control.Decode(api.output)
	if err != nil || response.Kind != "receipt" || response.Identity != (control.WireIdentity{LaunchID: config.Identity.LaunchID, Epoch: config.Identity.Epoch, PolicyDigest: config.Identity.PolicyDigest}) || !bytes.Contains(response.Payload, []byte(`"accepted":true`)) {
		t.Fatal("bound stop response missing")
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
	if err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop}); err != ErrUnconfirmed || dispatch.calls != 1 {
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
			err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop})
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
				if err = e.Exchange(ctx); err != ErrFatal {
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
	if err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.ReadSnapshot}); err != nil {
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
			if err := execute(e, ctx, command); err != ErrDenied || dispatch.calls != 0 {
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
		err := execute(e, ctx, Command{Generation: config.Generation, Identity: config.Identity, Operation: control.Stop})
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
		if err != ErrDenied || dispatch.calls != 0 {
			t.Fatal("cancelled request dispatched after close")
		}
	case <-ctx.Done():
		t.Fatal("token scope did not finish")
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Exchange(ctx); err != ErrClosed {
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
