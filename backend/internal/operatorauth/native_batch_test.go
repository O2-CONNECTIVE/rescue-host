package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type memoryBatchReport struct {
	events     *[]string
	releaseErr error
}

func (p *memoryBatchReport) childWriter() uintptr { return 61 }
func (p *memoryBatchReport) releaseWriter(context.Context) error {
	*p.events = append(*p.events, "release-writers")
	return p.releaseErr
}
func (p *memoryBatchReport) drain(context.Context) (reportResult, error) {
	*p.events = append(*p.events, "drain-eof")
	return reportResult{requestFrame(), true}, nil
}
func (p *memoryBatchReport) close(context.Context) error {
	*p.events = append(*p.events, "close-report")
	return nil
}

type memoryBatchChild struct {
	events       *[]string
	closeErr     error
	resumeCalled bool
}

func (c *memoryBatchChild) resume(context.Context) error {
	c.resumeCalled = true
	*c.events = append(*c.events, "resume")
	return nil
}
func (c *memoryBatchChild) recheck(context.Context) error {
	*c.events = append(*c.events, "binding")
	return nil
}
func (c *memoryBatchChild) waitExit(context.Context) (uint32, error) {
	*c.events = append(*c.events, "wait")
	return 0, nil
}
func (c *memoryBatchChild) terminate() error { *c.events = append(*c.events, "terminate"); return nil }
func (c *memoryBatchChild) close(context.Context) error {
	*c.events = append(*c.events, "close-child")
	return c.closeErr
}
func batchFixture(t *testing.T) (nativeBatchSpec, batchOwner, nativeBatchOps, *[]string, *memoryBatchReport, *memoryBatchChild) {
	t.Helper()
	_, cancel, l, _ := launchFixture()
	cancel()
	r := reportSetup()
	l.helperUserSID = r.helperSID
	l.operatorRead = 0
	l.reportWrite = 0
	s := nativeBatchSpec{input: 31, launch: l, report: r}
	events := []string{}
	p := &memoryBatchReport{events: &events}
	c := &memoryBatchChild{events: &events}
	o := batchOwner{revoke: func() error { events = append(events, "revoke"); return nil }, reclaim: func(context.Context) error { events = append(events, "reclaim-workload"); return nil }}
	k := nativeBatchOps{
		parent: func(context.Context) error { return nil }, clean: func() error { return nil },
		newReport: func(context.Context, reportConfig) (batchReport, error) {
			events = append(events, "report")
			return p, nil
		},
		duplicateRead: func(uintptr) (uintptr, error) { events = append(events, "duplicate-read"); return 32, nil },
		start: func(_ context.Context, l nativeLaunchSpec) (batchChild, error) {
			events = append(events, "suspended")
			if l.operatorRead != 32 || l.reportWrite != 61 {
				return nil, ErrDenied
			}
			return c, nil
		},
		closeHandle: func(h uintptr) error {
			if h == 31 {
				events = append(events, "close-original-read")
			} else if h == 32 {
				events = append(events, "close-child-read-copy")
			} else {
				events = append(events, "close-foreign")
			}
			return nil
		},
	}
	return s, o, k, &events, p, c
}

func TestNativeBatchWiringReleasesParentCopiesBeforeResume(t *testing.T) {
	s, o, k, events, _, _ := batchFixture(t)
	b, err := startBatch(requestContext(t), s, o, k)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"report", "duplicate-read", "suspended", "release-writers", "close-child-read-copy", "close-original-read", "resume", "binding"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatal(*events)
	}
	r, err := b.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Consume(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	want = append(want, "binding", "drain-eof", "wait", "binding", "close-report", "close-child")
	if !reflect.DeepEqual(*events, want) {
		t.Fatal(*events)
	}
}

func TestNativeBatchRejectsDuplicateAliasingWithoutClosingBorrowedToken(t *testing.T) {
	for _, kind := range []string{"input", "token", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			s, o, k, events, _, _ := batchFixture(t)
			value := s.input
			if kind == "token" {
				value = s.launch.primaryToken
			}
			if kind == "invalid" {
				value = ^uintptr(0)
			}
			k.duplicateRead = func(uintptr) (uintptr, error) { return value, ErrDenied }
			b, err := startBatch(requestContext(t), s, o, k)
			if b == nil || err == nil {
				t.Fatal("invalid duplicate accepted")
			}
			closedInput, closedForeign := 0, 0
			for _, e := range *events {
				if e == "close-original-read" {
					closedInput++
				}
				if e == "close-foreign" {
					closedForeign++
				}
			}
			if closedInput != 1 || closedForeign != 0 {
				t.Fatal("closed alias or borrowed token", *events)
			}
		})
	}
}

func TestNativeBatchReleaseFailureRevokesAndDoesNotResume(t *testing.T) {
	s, o, k, events, p, c := batchFixture(t)
	p.releaseErr = ErrUnconfirmed
	b, err := startBatch(requestContext(t), s, o, k)
	if b == nil || err == nil || c.resumeCalled {
		t.Fatal("release failure resumed helper")
	}
	var revoke, terminate int
	for i, e := range *events {
		if e == "revoke" {
			revoke = i
		}
		if e == "terminate" {
			terminate = i
		}
	}
	if revoke == 0 || terminate <= revoke {
		t.Fatal("termination preceded revocation", *events)
	}
	if _, e := b.Collect(requestContext(t)); !errors.Is(e, ErrDenied) {
		t.Fatal("failed batch revived")
	}
}
