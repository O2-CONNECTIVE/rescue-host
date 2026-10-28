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
	events *[]string
	err    error
}

func (p *testServerPeer) Recheck(context.Context) error {
	*p.events = append(*p.events, "server-recheck")
	return p.err
}
func (p *testServerPeer) Close(context.Context) error {
	*p.events = append(*p.events, "server-close")
	return nil
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
