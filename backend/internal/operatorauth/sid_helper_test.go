package operatorauth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func sidHelperFixture() (*Helper, *testThread, *[]string, *[]byte) {
	h, thread, events, published := helperFixture()
	h.profile = helperProfile{sid: fixedSIDPolicy(), launchID: strings.Repeat("a", 64)}
	thread.projection = tokenProjection{user: "S-1-5-21-1", authentication: 17, session: 3, integrity: 8192, kind: 2, level: 1}
	return h, thread, events, published
}

func TestSIDHelperAuthenticatesActualLogonAndSealsReportAfterRevert(t *testing.T) {
	for _, auth := range []uint64{17, 29} {
		h, thread, events, published := sidHelperFixture()
		thread.projection.authentication = auth
		if err := h.Run(requestContext(t)); err != nil {
			t.Fatal(err)
		}
		frame, actual, err := decodeAuthenticatedReport(*published, h.profile.launchID, h.profile.sid)
		if err != nil || string(frame) != string(requestFrame()) || actual.authentication != auth || actual.session != 3 {
			t.Fatal("actual authenticated subject lost", err)
		}
		order := strings.Join(*events, ",")
		if !strings.Contains(order, "token-close,revert,clean,peer,publish") {
			t.Fatal("report before revert", order)
		}
	}
}

func TestSIDHelperRejectsUntrustedTokenAndRealAuthenticationFailure(t *testing.T) {
	for _, name := range []string{"sid", "zero-auth", "kind", "level", "integrity", "restricted", "appcontainer", "token-error", "revert-error", "cancel"} {
		t.Run(name, func(t *testing.T) {
			h, thread, _, published := sidHelperFixture()
			ctx, cancel := context.WithCancel(requestContext(t))
			defer cancel()
			switch name {
			case "sid":
				thread.projection.user = "S-1-5-21-3"
			case "zero-auth":
				thread.projection.authentication = 0
			case "kind":
				thread.projection.kind = 1
			case "level":
				thread.projection.level = 3
			case "integrity":
				thread.projection.integrity = 0
			case "restricted":
				thread.projection.restricted = true
			case "appcontainer":
				thread.projection.appContainer = true
			case "token-error":
				thread.tokenErr = ErrDenied
			case "revert-error":
				thread.revertErr = ErrDenied
			case "cancel":
				thread.onToken = cancel
			}
			if err := h.Run(ctx); err == nil || len(*published) != 0 {
				t.Fatal("unverified report published", err)
			}
		})
	}
	h, _, _, published := sidHelperFixture()
	h.profile.sid.GuestSID = h.profile.sid.OperatorSID
	if err := h.Run(requestContext(t)); !errors.Is(err, ErrDenied) || len(*published) != 0 {
		t.Fatal("invalid B policy fell back", err)
	}
}

func TestSIDReportTransportRetainsEnvelopeLargerThanPublicFrameBudget(t *testing.T) {
	report := []byte(strings.Repeat("a", 20000) + "\n")
	writerKernel := &reportFake{replies: []reportReply{{n: uint32(len(report))}}}
	writer, err := newMessageEndpoint(22, endpointClientWrite, 1e9, writerKernel)
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.write(requestContext(t), report); err != nil {
		t.Fatal("internal envelope constrained by public budget", err)
	}
	readerKernel := &reportFake{replies: []reportReply{{data: string(report), n: uint32(len(report))}, {err: errReportBrokenPipe}}}
	pair, err := newReportPair(requestContext(t), reportSetup(), readerKernel)
	if err != nil {
		t.Fatal(err)
	}
	if err = pair.releaseWriter(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	got, err := pair.drain(requestContext(t))
	if err != nil || string(got.frame) != string(report) || !got.eof {
		t.Fatal("internal report truncated", err)
	}
	if err = pair.close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

type sidDeliveredReport struct {
	*memoryBatchReport
	published *[]byte
}

func (r *sidDeliveredReport) drain(context.Context) (reportResult, error) {
	return reportResult{frame: append([]byte(nil), (*r.published)...), eof: true}, nil
}

// 同launchのmemory transportを接続する。実Windows保護やnative成功の証拠ではない。
func TestSIDProfileDeliveryAuthenticatesOnlyActualHelperMessage(t *testing.T) {
	ctx, receiver, _ := receiverSetup(t)
	data := make(chan []byte, 1)
	api := &profileSyncFixture{transferFn: func(kind profileSyncKind, _ uintptr, b []byte) (uint32, error) {
		if kind == profileSyncWrite {
			data <- append([]byte(nil), b...)
			return uint32(len(b)), nil
		}
		chunk, ok := <-data
		if !ok {
			return 0, errProfileSyncEOF
		}
		return uint32(copy(b, chunk)), nil
	}}
	receiver.ops.start = func(c context.Context, h uintptr, b []byte) (*profileSyncOwner, error) {
		return startProfileSync(c, api, profileSyncRead, h, b)
	}
	kernel := &profilePipeKernelFixture{inheritFn: func(uintptr) (uintptr, error) { return 77, nil }, closeFn: func(h uintptr) error {
		if h == 52 {
			close(data)
		}
		return nil
	}}
	s, owner, ops, _, report, child := batchFixture(t)
	s.sidPolicy = fixedSIDPolicy()
	s.report.guestSID = s.sidPolicy.GuestSID
	producer, err := newSIDProfileProvider(s.launch.launchID, s.sidPolicy, s.launch.cleanup)
	if err != nil {
		t.Fatal(err)
	}
	s.producer = producer
	ops.newProfile = func(c context.Context) (batchProfile, error) { return newProfilePipe(c, producer, kernel, api) }
	h, _, _, published := sidHelperFixture()
	ops.newReport = func(context.Context, reportConfig) (batchReport, error) {
		return &sidDeliveredReport{report, published}, nil
	}
	finished := make(chan error, 1)
	ops.start = func(_ context.Context, l nativeLaunchSpec) (batchChild, error) {
		if l.profileRead != 77 || l.launchID != s.launch.launchID {
			return nil, ErrDenied
		}
		return &deliveryChild{child, func() { go func() { finished <- receiver.run(ctx, h.ops) }() }}, nil
	}
	b, err := startBatch(ctx, s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
	case <-ctx.Done():
		t.Fatal("helper did not join")
	}
	if err != nil {
		t.Fatal(err)
	}
	proof, err := b.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frame, binding, err := b.Collector().ConsumeAuthenticated(ctx, proof)
	if err != nil || string(frame) != string(requestFrame()) || !binding.Matches(b.Collector(), owner.owner, owner.issuer) {
		t.Fatal("authenticated subject did not survive same-launch path", err)
	}
	if err = b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

type deniedImpersonationThread struct{ *testThread }

func (*deniedImpersonationThread) impersonate(uintptr) error { return ErrDenied }
func TestSIDHelperDoesNotReplaceActualMessageAuthenticationWithPolicy(t *testing.T) {
	h, thread, _, published := sidHelperFixture()
	h.ops.thread = &deniedImpersonationThread{thread}
	if err := h.Run(requestContext(t)); !errors.Is(err, ErrDenied) || len(*published) != 0 {
		t.Fatal("static SID policy substituted for impersonation", err)
	}
}
func TestSIDPublicFrameLimitRemainsSixteenKiBInsideBoundedReport(t *testing.T) {
	h, _, _, published := sidHelperFixture()
	frame := requestFrame()
	frame = append(frame[:len(frame)-1], []byte(strings.Repeat(" ", 16384-len(frame)))...)
	frame = append(frame, '\n')
	h.ops.read = func(context.Context) ([]byte, error) { return frame, nil }
	if err := h.Run(requestContext(t)); err != nil {
		t.Fatal("exact public boundary rejected", err)
	}
	if len(*published) <= 16384 || len(*published) > 32768 {
		t.Fatal("unexpected internal report size", len(*published))
	}
	exact, _, err := decodeAuthenticatedReport(*published, h.profile.launchID, h.profile.sid)
	if err != nil || string(exact) != string(frame) {
		t.Fatal("raw public bytes changed", err)
	}
	tooLarge := append([]byte(" "), frame...)
	h, _, _, published = sidHelperFixture()
	h.ops.read = func(context.Context) ([]byte, error) { return tooLarge, nil }
	if err = h.Run(requestContext(t)); err == nil || len(*published) != 0 {
		t.Fatal("public budget widened", err)
	}
	for _, size := range []int{32768, 32769} {
		data := []byte(strings.Repeat("a", size-1) + "\n")
		k := &reportFake{replies: []reportReply{{n: uint32(size)}}}
		w, err := newMessageEndpoint(22, endpointClientWrite, 1e9, k)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.write(requestContext(t), data); (err == nil) != (size == 32768) {
			t.Fatal("internal boundary wrong", size, err)
		}
	}
}

func TestSIDHelperAcceptsActualSessionIntegrityAndBothMessageLevels(t *testing.T) {
	for _, level := range []uint32{1, 2} {
		h, thread, _, published := sidHelperFixture()
		thread.projection.session = 19
		thread.projection.integrity = 12288
		thread.projection.level = level
		if err := h.Run(requestContext(t)); err != nil {
			t.Fatal("post-authentication values treated as prior policy", err)
		}
		_, actual, err := decodeAuthenticatedReport(*published, h.profile.launchID, h.profile.sid)
		if err != nil || actual.session != 19 || actual.integrity != 12288 || actual.level != level {
			t.Fatal("actual projection changed", err)
		}
	}
}
