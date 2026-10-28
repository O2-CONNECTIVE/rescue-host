package operatorauth

import (
	"strings"
	"testing"
	"time"
)

// These are synthetic regression cases following the fixed red/green slices.
func TestReportRejectsEmptyPartialAndOversizedMessages(t *testing.T) {
	for _, r := range []reportReply{{}, {data: "partial", n: 7}, {n: 16385}, {data: "x\n", n: 2, err: errReportMoreData}, {data: "x\n", n: 2, err: errReportBrokenPipe}} {
		k := &reportFake{replies: []reportReply{r}}
		p, err := newReportPair(reportContext(t), reportSetup(), k)
		if err != nil {
			t.Fatal(err)
		}
		if err = p.releaseWriter(reportContext(t)); err != nil {
			t.Fatal(err)
		}
		if got, err := p.drain(reportContext(t)); err == nil || got.eof {
			t.Fatalf("invalid message accepted: %+v", r)
		}
		if err = p.close(reportContext(t)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestReportAcceptsExactSixteenKiBMessageThenActualEOF(t *testing.T) {
	frame := strings.Repeat("a", 16383) + "\n"
	k := &reportFake{replies: []reportReply{{data: frame, n: 16384}, {err: errReportBrokenPipe}}}
	p, err := newReportPair(reportContext(t), reportSetup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.releaseWriter(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	got, err := p.drain(reportContext(t))
	if err != nil || !got.eof || string(got.frame) != frame {
		t.Fatalf("frame size=%d error=%v", len(got.frame), err)
	}
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestReportCannotDrainWithParentWriterCopies(t *testing.T) {
	k := &reportFake{}
	p, err := newReportPair(reportContext(t), reportSetup(), k)
	if err != nil {
		t.Fatal(err)
	}
	if p.childWriter() != 13 {
		t.Fatal("writer capability unavailable")
	}
	if got, err := p.drain(reportContext(t)); err == nil || got.eof {
		t.Fatal("accepted drain while parent held writer copies")
	}
	if err = p.close(reportContext(t)); err != nil {
		t.Fatal(err)
	}
	if p.childWriter() != 0 {
		t.Fatal("closed writer capability available")
	}
}
func TestMessageEndpointRejectsPartialPublication(t *testing.T) {
	k := &reportFake{replies: []reportReply{{n: 2}}}
	e, err := newMessageEndpoint(22, endpointClientWrite, time.Second, k)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.write(reportContext(t), []byte("report\n")); err == nil {
		t.Fatal("partial publication accepted")
	}
	if len(k.closed) != 1 || k.closed[0] != 22 {
		t.Fatal("partial publication writer leaked")
	}
}
