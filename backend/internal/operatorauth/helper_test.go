package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func requestFrame() []byte {
	return []byte(`{"protocol":"rescue-host-service/1","type":"hello","sequence":"0","requestId":"11111111111111111111111111111111","clientNonce":"` + strings.Repeat("2", 64) + `"}` + "\n")
}
func requestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

type testThread struct {
	events                        *[]string
	projection                    tokenProjection
	cleanErr, tokenErr, revertErr error
	cleanCount                    int
	onToken                       func()
	onRevert                      func()
}

func (s *testThread) record(v string)           { *s.events = append(*s.events, v) }
func (s *testThread) lock()                     { s.record("lock") }
func (s *testThread) unlock()                   { s.record("unlock") }
func (s *testThread) clean() error              { s.cleanCount++; s.record("clean"); return s.cleanErr }
func (s *testThread) impersonate(uintptr) error { s.record("impersonate"); return nil }
func (s *testThread) token() (tokenProjection, error) {
	s.record("token-close")
	if s.onToken != nil {
		s.onToken()
	}
	return s.projection, s.tokenErr
}
func (s *testThread) revert() error {
	s.record("revert")
	if s.onRevert != nil {
		s.onRevert()
	}
	return s.revertErr
}
func (s *testThread) abort() { s.record("abort") }

func helperFixture() (*Helper, *testThread, *[]string, *[]byte) {
	events := []string{}
	p := tokenProjection{user: "operator", authentication: 7, session: 3, integrity: 0x2000, kind: 2, level: 1}
	thread := &testThread{events: &events, projection: p}
	var published []byte
	h := &Helper{profile: helperProfile{operator: p, guest: tokenProjection{user: "guest"}}, ops: helperOps{thread: thread, pipe: 9,
		check: func(context.Context) error { events = append(events, "peer"); return nil },
		read:  func(context.Context) ([]byte, error) { events = append(events, "read"); return requestFrame(), nil },
		publish: func(_ context.Context, b []byte) error {
			events = append(events, "publish")
			published = append([]byte(nil), b...)
			return nil
		}}}
	return h, thread, &events, &published
}

func TestHelperRestorationPanicAbortsWithoutPublishing(t *testing.T) {
	h, thread, events, published := helperFixture()
	thread.onRevert = func() { panic("synthetic restore failure") }
	var err error
	func() { defer func() { _ = recover() }(); err = h.Run(requestContext(t)) }()
	if !errors.Is(err, ErrFatal) || len(*published) != 0 || (*events)[len(*events)-1] != "abort" {
		t.Fatalf("err=%v order=%v report=%q", err, *events, *published)
	}
}

func TestHelperPeerPanicCannotReturnToReusableCaller(t *testing.T) {
	h, _, events, published := helperFixture()
	h.ops.check = func(context.Context) error { panic("synthetic native binding failure") }
	var err error
	func() { defer func() { _ = recover() }(); err = h.Run(requestContext(t)) }()
	if !errors.Is(err, ErrFatal) || len(*published) != 0 || (*events)[len(*events)-1] != "abort" {
		t.Fatalf("err=%v order=%v", err, *events)
	}
}

func TestHelperReportsOnlyAfterReadAuthenticationAndRestoration(t *testing.T) {
	h, _, events, published := helperFixture()
	if err := h.Run(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	want := []string{"lock", "clean", "peer", "read", "peer", "impersonate", "token-close", "revert", "clean", "peer", "publish", "unlock"}
	if !reflect.DeepEqual(*events, want) || string(*published) != string(requestFrame()) {
		t.Fatalf("order=%v report=%q", *events, *published)
	}
	if err := h.Run(requestContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("single-use helper reused", err)
	}
}
