package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testServerPeer struct {
	events  *[]string
	err     error
	onClose func()
}

func (p *testServerPeer) Recheck(context.Context) error {
	*p.events = append(*p.events, "server-recheck")
	return p.err
}
func (p *testServerPeer) Close(context.Context) error {
	*p.events = append(*p.events, "server-close")
	if p.onClose != nil {
		p.onClose()
	}
	return nil
}

func TestControllerCleanupCancellationCannotReturnSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(requestContext(t))
	defer cancel()
	events := []string{}
	p := &testServerPeer{events: &events, onClose: cancel}
	c := &Controller{cleanup: time.Second, ops: controllerOps{
		connect: func(context.Context) (serverPeer, error) { return p, nil },
		write:   func(context.Context, []byte) error { return nil },
		read:    func(context.Context) ([]byte, error) { return helloResponse(), nil },
	}}
	if b, err := c.Exchange(ctx, requestFrame()); b != nil || err == nil {
		t.Fatal("cleanup cancelled request accepted", err)
	}
}
func helloResponse() []byte {
	return []byte(`{"protocol":"rescue-host-service/1","type":"hello","sequence":"0","requestId":"11111111111111111111111111111111","clientNonce":"` + strings.Repeat("2", 64) + `","bootEpoch":"` + strings.Repeat("a", 64) + `","channelNonce":"` + strings.Repeat("b", 64) + `","requestLimit":"1"}` + "\n")
}
func TestControllerAuthenticatesRetainedServerBeforePayloadAndRechecksAfterIO(t *testing.T) {
	events := []string{}
	p := &testServerPeer{events: &events}
	c := &Controller{cleanup: time.Second, ops: controllerOps{
		connect: func(context.Context) (serverPeer, error) {
			events = append(events, "server-connect-auth")
			return p, nil
		},
		write: func(_ context.Context, b []byte) error {
			events = append(events, "write")
			if string(b) != string(requestFrame()) {
				return ErrDenied
			}
			return nil
		},
		read: func(context.Context) ([]byte, error) {
			events = append(events, "read-response")
			return helloResponse(), nil
		},
	}}
	b, err := c.Exchange(requestContext(t), requestFrame())
	if err != nil || string(b) != string(helloResponse()) {
		t.Fatal(err)
	}
	want := []string{"server-connect-auth", "server-recheck", "write", "server-recheck", "read-response", "server-recheck", "server-close"}
	if !reflect.DeepEqual(events, want) {
		t.Fatal(events)
	}
	if _, err = c.Exchange(requestContext(t), requestFrame()); !errors.Is(err, ErrDenied) {
		t.Fatal("reused exchange", err)
	}
}

type cleanupDeadlinePeer struct {
	*testServerPeer
	close func(context.Context) error
}

func (p *cleanupDeadlinePeer) Close(ctx context.Context) error { return p.close(ctx) }
func TestControllerCleanupDeadlineRetainsPeerWhileRequestIsLive(t *testing.T) {
	request, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := []string{}
	calls := 0
	peer := &cleanupDeadlinePeer{testServerPeer: &testServerPeer{events: &events}, close: func(ctx context.Context) error {
		calls++
		if calls == 1 {
			<-ctx.Done()
		}
		return nil
	}}
	controller := &Controller{cleanup: 5 * time.Millisecond, ops: controllerOps{
		connect: func(context.Context) (serverPeer, error) { return peer, nil },
		write:   func(context.Context, []byte) error { return nil },
		read:    func(context.Context) ([]byte, error) { return helloResponse(), nil },
	}}
	response, err := controller.Exchange(request, requestFrame())
	if request.Err() != nil {
		t.Fatal("test requires request to remain live while independent cleanup expires")
	}
	if response != nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("expired cleanup returned a successful response", err)
	}
	controller.mu.Lock()
	retained := controller.peer == peer
	controller.mu.Unlock()
	if !retained {
		t.Fatal("expired cleanup discarded the retained server peer")
	}
	cleanup, finish := context.WithTimeout(context.Background(), time.Second)
	defer finish()
	if err := controller.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	released := controller.peer == nil
	controller.mu.Unlock()
	if !released || calls != 2 {
		t.Fatal("sameowner fresh cleanup failed to release original peer")
	}
}
