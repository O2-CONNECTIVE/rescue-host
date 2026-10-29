package serviceready

import (
	"context"
	"errors"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

type inspectionTestHandle struct {
	endpointTestHandle
	state                      inspectionState
	inspectErr, subjectErr     error
	onInspect                  func()
	inspectCalls, subjectCalls int
}

func TestInspectionLeaseMatchesOnlyFrozenLiveProfile(t *testing.T) {
	e, k := inspectionFixture(t)
	l, err := e.NewInspectionLease(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	want := cloneConfig(e.query.c).Profile
	if !l.MatchesProfile(want) || l.MatchesProfile(bootstrapauth.Profile{}) {
		t.Fatal("profile binding mismatch")
	}
	other := cloneConfig(e.query.c).Profile
	other.SupervisorImage.Path += ".other"
	if l.MatchesProfile(other) {
		t.Fatal("different frozen profile matched")
	}
	queries := k.inspectionPipe.inspection.inspectCalls
	l.mu.Lock()
	if l.MatchesProfile(want) {
		t.Fatal("busy lease admitted")
	}
	l.mu.Unlock()
	for _, state := range []string{"failed", "unknown", "closed"} {
		l.failed, l.unknown, l.closed = state == "failed", state == "unknown", state == "closed"
		if l.MatchesProfile(want) {
			t.Fatal("invalid lease matched", state)
		}
	}
	l.failed, l.unknown, l.closed = false, false, false
	copyLease := &InspectionLease{self: l, endpoint: e, binding: l.binding}
	if copyLease.MatchesProfile(want) {
		t.Fatal("copied lease matched")
	}
	if k.inspectionPipe.inspection.inspectCalls != queries {
		t.Fatal("pure match issued native query")
	}
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func (h *inspectionTestHandle) inspectInspection(context.Context, inspectionBinding) (inspectionState, error) {
	h.inspectCalls++
	if h.onInspect != nil {
		h.onInspect()
	}
	return h.state, h.inspectErr
}
func (h *inspectionTestHandle) inspectionSubject(context.Context, inspectionBinding) (inspectionSubject, error) {
	h.subjectCalls++
	return h.state.subject, h.subjectErr
}
func (h *inspectionTestHandle) borrowInspection() (uintptr, error) { return h.handle, nil }

type inspectionTestPipe struct {
	*endpointTestPipe
	inspection inspectionTestHandle
}

func (p *inspectionTestPipe) duplicate(access uint32) (endpointHandle, error) {
	if access != endpointInspectionAccess {
		return p.endpointTestPipe.duplicate(access)
	}
	p.duplicates = append(p.duplicates, access)
	if p.onDuplicate != nil {
		p.onDuplicate()
	}
	return &p.inspection, p.duplicateErr
}

type inspectionTestKernel struct {
	*endpointTestKernel
	inspectionPipe *inspectionTestPipe
}

func (k *inspectionTestKernel) createEndpoint(string, string) (endpointDuplex, error) {
	k.listenerCreates++
	return k.inspectionPipe, nil
}
func inspectionFixture(t *testing.T) (*Endpoint, *inspectionTestKernel) {
	t.Helper()
	f, k := endpointFixture(t)
	p := &inspectionTestPipe{endpointTestPipe: &k.pipe}
	api := &inspectionTestKernel{endpointTestKernel: k, inspectionPipe: p}
	f.api = api
	e, err := f.PrepareEndpoint(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.AwaitConnected(testContext(t)); err != nil {
		t.Fatal(err)
	}
	p.inspection.endpointTestHandle = endpointTestHandle{handle: 91, access: endpointInspectionAccess}
	p.inspection.state = inspectionState{kind: 3, access: endpointInspectionAccess, serverPID: e.query.pid, currentPID: e.query.pid, creation: e.query.creation, security: listenerSecurity(e.query.c.Profile), subject: inspectionSubject{user: e.query.c.Profile.Keys.ProcessUserSID, kind: 1, serviceAttributes: 4, tokenID: 1, authenticationID: 2, modifiedID: 3}}
	return e, api
}
func TestInspectionLeaseSurvivesSplitMoveWithoutReadingOrOriginalScopeQueries(t *testing.T) {
	e, k := inspectionFixture(t)
	l, err := e.NewInspectionLease(testContext(t))
	if err != nil || l == nil {
		t.Fatal("connected inspection unavailable", err)
	}
	s, err := e.Split(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if h, err := s.Move(testContext(t)); err != nil || h != 88 {
		t.Fatal(h, err)
	}
	k.thread++ // inspection's separate actor never re-enters Endpoint's affine scope.
	before := k.scope.checks
	if h, err := l.Borrow(testContext(t)); err != nil || h != 91 {
		t.Fatal(h, err)
	}
	if err = l.Recheck(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.scope.checks != before || k.pipe.read.checks != 2 {
		t.Fatal("inspection borrowed readable endpoint or original scope")
	}
	k.thread--
	if r, err := e.CloseEndpoint(testContext(t)); r != nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("endpoint ignored active lease", r, err)
	}
	if k.scope.closes != 0 || k.pipe.writer.closes != 0 {
		t.Fatal("original refs closed before lease ack")
	}
	if err = l.Close(testContext(t)); err != nil {
		t.Fatal("revocation blocked independent close", err)
	}
	if r, err := e.CloseEndpoint(testContext(t)); err != nil || r == nil {
		t.Fatal(r, err)
	}
	if k.inspectionPipe.inspection.closes != 1 || e.inspectionActive.Load() != 0 {
		t.Fatal("duplicate not closed exactly once")
	}
}
