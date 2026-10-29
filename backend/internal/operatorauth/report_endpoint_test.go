package operatorauth

import (
	"context"
	"testing"
	"time"
)

func TestMessageEndpointReadsOnlyOneMessage(t *testing.T) {
	k := &reportFake{replies: []reportReply{{data: "request\n", n: 8}}}
	e, err := newMessageEndpoint(21, endpointServerRead, time.Second, k)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.read(reportContext(t))
	if err != nil || string(b) != "request\n" {
		t.Fatalf("read=%q error=%v", b, err)
	}
	if _, err = e.read(reportContext(t)); err == nil {
		t.Fatal("second read accepted")
	}
	if err = e.write(reportContext(t), []byte("report\n")); err == nil {
		t.Fatal("write accepted on server reader")
	}
	if err = e.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestMessageEndpointPublishesOnceAndClosesWriter(t *testing.T) {
	k := &reportFake{replies: []reportReply{{n: 7}}}
	e, err := newMessageEndpoint(22, endpointClientWrite, time.Second, k)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.write(reportContext(t), []byte("report\n")); err != nil {
		t.Fatal(err)
	}
	if len(k.closed) != 1 || k.closed[0] != 22 {
		t.Fatal("writer retained after completed publish")
	}
	if err = e.write(reportContext(t), []byte("report\n")); err == nil {
		t.Fatal("second publish accepted")
	}
	if _, err = e.read(context.Background()); err == nil {
		t.Fatal("read accepted on writer")
	}
}

type reportAdoptFailure struct {
	reportFake
	validation, clear error
}

func (k *reportAdoptFailure) validate(uintptr, endpointRole) error { return k.validation }
func (k *reportAdoptFailure) clearInherit(uintptr) error           { return k.clear }
func TestMessageAdoptionKeepsCallerOwnershipUntilValidation(t *testing.T) {
	k := &reportAdoptFailure{validation: ErrDenied}
	e, err := newMessageEndpoint(30, endpointClientWrite, time.Second, k)
	if e != nil || err == nil || len(k.closed) != 0 {
		t.Fatal("rejected handle was adopted or closed")
	}
	k = &reportAdoptFailure{clear: ErrDenied}
	e, err = newMessageEndpoint(31, endpointClientWrite, time.Second, k)
	if e == nil || err == nil {
		t.Fatal("inherit-clear failure lost its owner or accepted handle")
	}
	if err = e.close(reportContext(t)); err != nil || len(k.closed) != 1 || k.closed[0] != 31 {
		t.Fatal("adopted handle not reclaimed")
	}
}
