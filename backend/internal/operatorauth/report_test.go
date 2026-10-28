package operatorauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type reportReply struct {
	data    string
	n       uint32
	err     error
	pending bool
}
type reportFake struct {
	replies          []reportReply
	closed           []uintptr
	descriptor, name string
}

func (*reportFake) primary() (string, map[string]uint32, error) {
	return "S-1-5-21-1", map[string]uint32{"S-1-5-80-2": 4}, nil
}
func (f *reportFake) create(n, d string) (uintptr, error) {
	f.name, f.descriptor = n, d
	return 11, nil
}
func (*reportFake) open(string) (uintptr, error)         { return 12, nil }
func (*reportFake) duplicate(uintptr) (uintptr, error)   { return 13, nil }
func (*reportFake) clearInherit(uintptr) error           { return nil }
func (*reportFake) validate(uintptr, endpointRole) error { return nil }
func (f *reportFake) close(h uintptr) error              { f.closed = append(f.closed, h); return nil }
func (f *reportFake) start(k reportKind, _ uintptr, b []byte) (reportOperation, error) {
	if k == reportConnect {
		return &reportFakeOp{}, nil
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	copy(b, r.data)
	return &reportFakeOp{reply: r}, nil
}

type reportFakeOp struct{ reply reportReply }

func (o *reportFakeOp) poll() (bool, uint32, error) { return !o.reply.pending, o.reply.n, o.reply.err }
func (*reportFakeOp) cancel() error                 { return nil }
func (*reportFakeOp) release() error                { return nil }
func reportContext(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return c
}
func reportSetup() reportConfig {
	return reportConfig{"S-1-5-21-1", "S-1-5-80-2", "S-1-5-21-3", "S-1-5-21-4", 20 * time.Millisecond}
}
func TestReportDrainRequiresActualBrokenPipe(t *testing.T) {
	for _, tt := range []struct {
		name  string
		eof   reportReply
		valid bool
	}{
		{"real EOF", reportReply{err: errReportBrokenPipe}, true},
		{"empty message", reportReply{}, false},
		{"extra message", reportReply{data: "x", n: 1}, false},
		{"cancelled EOF", reportReply{err: errors.Join(errReportBrokenPipe, context.Canceled)}, false},
		{"nonzero broken pipe", reportReply{n: 1, err: errReportBrokenPipe}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			k := &reportFake{replies: []reportReply{{data: "frame\n", n: 6}, tt.eof}}
			p, e := newReportPair(reportContext(t), reportSetup(), k)
			if e != nil {
				t.Fatal(e)
			}
			if e = p.releaseWriter(reportContext(t)); e != nil {
				t.Fatal(e)
			}
			got, e := p.drain(reportContext(t))
			if (e == nil) != tt.valid || got.eof != tt.valid {
				t.Fatalf("report=%+v error=%v", got, e)
			}
			if _, e = p.drain(reportContext(t)); e == nil {
				t.Fatal("second drain accepted")
			}
			if e = p.close(reportContext(t)); e != nil {
				t.Fatal(e)
			}
		})
	}
}
