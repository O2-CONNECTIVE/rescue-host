package servicehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func TestSIDPolicyBindsOnlySameUnusedOwnerAndKeepsHelperClosed(t *testing.T) {
	s, err := NewPreviewService(previewConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	o := &confirmationOwnerModel{service: s}
	p := operatorauth.SIDPolicy{OperatorSID: s.control.factories.operatorSID, GuestSID: s.control.factories.guestSID}
	if s.BindOperatorSIDPolicy(o, p) == nil {
		t.Fatal("policy accepted without original outer owner")
	}
	if err = s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if s.BindOperatorSIDPolicy(&confirmationOwnerModel{service: s}, p) == nil {
		t.Fatal("foreign owner supplied policy")
	}
	wrong := p
	wrong.OperatorSID = wrong.GuestSID
	if s.BindOperatorSIDPolicy(o, wrong) == nil {
		t.Fatal("untrusted SID overrode fixed Record")
	}
	if err = s.BindOperatorSIDPolicy(o, p); err != nil {
		t.Fatal(err)
	}
	f := s.control.factories.batch
	if f == nil || !f.MatchesSIDPolicy(p.OperatorSID, p.GuestSID) || f.RuntimeConfigured() {
		t.Fatal("policy was omitted or promoted to runtime")
	}
	if s.BindOperatorSIDPolicy(o, p) == nil {
		t.Fatal("policy replaced after binding")
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		t.Fatal("missing helper acquired SCM/endpoint")
		return serviceControlWitness{}, ErrProvisioningRejected
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.auth.exchangeControlEndpoint(ctx); !errors.Is(err, ErrProvisioningUnconfigured) || s.auth.attempts != 0 || s.auth.active != nil {
		t.Fatal("missing helper acquired request/endpoint", err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// This is the existing private auth/SCM scheduling model, not a fabricated
// native OperatorBinding, actual helper report or Windows authentication proof.
type sidSubjectModel struct {
	collector      *operatorauth.Collector
	owner          *serviceauthowner.Owner
	issuer         *ephemeral.Issuer
	user, guest    string
	authentication uint64
}

func (m *sidSubjectModel) matches(c *operatorauth.Collector, o *serviceauthowner.Owner, i *ephemeral.Issuer) bool {
	return m != nil && c == m.collector && o == m.owner && i == m.issuer
}
func (m *sidSubjectModel) matchesPolicy(op, guest string) bool {
	return m != nil && m.user == op && m.guest == guest && op != guest
}
func (m *sidSubjectModel) same(other serviceOperatorSubject) bool {
	n, ok := other.(*sidSubjectModel)
	return ok && m.user == n.user && m.guest == n.guest && m.authentication == n.authentication
}

func sidConfirmationFixture(t *testing.T) (*Service, *serviceAuthAdapter) {
	t.Helper()
	c := previewConfigFixture(t)
	c.RequestLimit, c.LaunchLimit = 8, 9
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	o := &confirmationOwnerModel{service: s, resolve: func(_ context.Context, ticket PreviewConfirmationTicket) error {
		_, e := ticket.PreviewFor(s)
		return e
	}}
	if err = s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if err = s.BindOperatorSIDPolicy(o, operatorauth.SIDPolicy{OperatorSID: c.Ready.Profile.Keys.OperatorSID, GuestSID: c.Ready.Profile.Keys.GuestSID}); err != nil {
		t.Fatal(err)
	}
	s.cfg.Budgets.Observe = time.Hour
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for s.Snapshot().State != Running {
		select {
		case <-ctx.Done():
			t.Fatal("SCM model not running")
		case <-time.After(time.Millisecond):
		}
	}
	t.Cleanup(func() {
		ctx, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		if e := s.Close(ctx); e != nil && !fatalServiceThread(e) {
			t.Error(e)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("SCM model not joined")
		}
	})
	return s, s.auth
}
func sidBatch(a *serviceAuthAdapter, frame string, id uint64) (serviceAuthBatchOps, *sidSubjectModel) {
	b := fixtureAuthBatch(a, frame)
	p := &sidSubjectModel{collector: b.collector, owner: b.owner, issuer: b.issuer, user: a.service.control.factories.operatorSID, guest: a.service.control.factories.guestSID, authentication: id}
	b.consumeAuthenticated = func(context.Context, *operatorauth.Read) ([]byte, serviceOperatorSubject, error) {
		return []byte(frame), p, nil
	}
	b.consume = func(context.Context, *operatorauth.Read) ([]byte, error) { return nil, ErrProvisioningRejected }
	return b, p
}
func sidExchange(t *testing.T, a *serviceAuthAdapter, frame string, id uint64) servicecontrol.UntrustedResponse {
	t.Helper()
	b, _ := sidBatch(a, frame, id)
	var reply servicecontrol.UntrustedResponse
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := a.exchangeOps(ctx, b, func(_ context.Context, raw []byte) error {
		var err error
		reply, err = servicecontrol.DecodeUntrustedResponseFrame(raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return reply
}
func TestSIDChangedLogonCannotInheritExistingGrantOrChannel(t *testing.T) {
	s, a := sidConfirmationFixture(t)
	grantRuntimeFixture(t, s)
	hello := sidExchange(t, a, dispatchHello, 11)
	prepared := sidExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent), 11)
	approved := sidExchange(t, a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"), 11)
	if approved.Approval == nil || s.issuer.Snapshot().Generation != 1 {
		t.Fatal("model did not reach real issuer Prepare")
	}
	snap := sidExchange(t, a, dispatchRequest(hello, 3, "44444444444444444444444444444444", servicecontrol.Status), 11)
	if snap.Snapshot == nil || snap.Snapshot.Binding == nil {
		t.Fatal("same identity lost its binding")
	}
	old := a.operator
	digest := s.preview.descriptor.ConfigDigest
	b, _ := sidBatch(a, dispatchRequest(hello, 4, "55555555555555555555555555555555", servicecontrol.Status), 22)
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	var writes atomic.Int32
	if err := a.exchangeOps(ctx, b, func(context.Context, []byte) error { writes.Add(1); return nil }); err == nil {
		t.Fatal("same SID different logon inherited channel")
	}
	if writes.Load() != 0 || a.operator != old || !s.issuer.Snapshot().Closed || s.preview.descriptor.ConfigDigest != digest {
		t.Fatal("changed logon transferred grant/Record or published reply")
	}

	next, na := sidConfirmationFixture(t)
	fresh := sidExchange(t, na, dispatchHello, 22)
	if fresh.BootEpoch == hello.BootEpoch || next.issuer.Snapshot().Generation != 0 || next.preview.intent != nil || next.ownerIntent != nil || next.reservation != nil {
		t.Fatal("new eligible logon inherited old approval")
	}
	stale, _ := sidBatch(na, confirmationFrame(hello, prepared.Intent, 1, "66666666666666666666666666666666"), 22)
	if err := na.exchangeOps(ctx, stale, func(context.Context, []byte) error { t.Error("old endpoint/intent replied on new channel"); return nil }); err == nil {
		t.Fatal("old approval reused after reconnect")
	}
}
func TestSIDBindingRejectsForeignOrMissingSubjectBeforeDispatch(t *testing.T) {
	for _, kind := range []string{"sid", "collector", "owner", "issuer", "nil", "consume"} {
		t.Run(kind, func(t *testing.T) {
			s, a := sidConfirmationFixture(t)
			b, p := sidBatch(a, dispatchHello, 11)
			switch kind {
			case "sid":
				p.user = p.guest
			case "collector":
				p.collector = new(operatorauth.Collector)
			case "owner":
				p.owner = nil
			case "issuer":
				p.issuer = nil
			case "nil":
				b.consumeAuthenticated = func(context.Context, *operatorauth.Read) ([]byte, serviceOperatorSubject, error) {
					return []byte(dispatchHello), nil, nil
				}
			case "consume":
				b.consumeAuthenticated = func(context.Context, *operatorauth.Read) ([]byte, serviceOperatorSubject, error) {
					return nil, nil, ErrProvisioningRejected
				}
			}
			ctx, end := context.WithTimeout(context.Background(), time.Second)
			defer end()
			if err := a.exchangeOps(ctx, b, func(context.Context, []byte) error { t.Error("invalid subject wrote reply"); return nil }); err == nil || a.operator != nil || !s.issuer.Snapshot().Closed {
				t.Fatal("invalid principal adopted", err)
			}
		})
	}
}
func TestSIDConfiguredAdapterRejectsLegacyConsumeBeforeAcquisition(t *testing.T) {
	_, a := sidConfirmationFixture(t)
	b := fixtureAuthBatch(a, dispatchHello)
	var starts atomic.Int32
	b.start = func(context.Context) error { starts.Add(1); return nil }
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := a.exchangeOps(ctx, b, func(context.Context, []byte) error { t.Error("legacy reply under SID policy"); return nil }); err == nil || starts.Load() != 0 || a.attempts != 0 {
		t.Fatal("SID policy bypassed via legacy consume", err)
	}
}

func TestSIDFreshChannelStillNeedsFreshApproval(t *testing.T) {
	s, a := sidConfirmationFixture(t)
	grantRuntimeFixture(t, s)
	hello := sidExchange(t, a, dispatchHello, 22)
	status := sidExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.Status), 22)
	if status.Snapshot == nil || status.Snapshot.Binding != nil || s.issuer.Snapshot().Generation != 0 {
		t.Fatal("eligibility became old approval")
	}
	prepared := sidExchange(t, a, dispatchRequest(hello, 2, "33333333333333333333333333333333", servicecontrol.PrepareIntent), 22)
	if s.issuer.Snapshot().Generation != 0 {
		t.Fatal("preview issued a grant without fresh consent")
	}
	approved := sidExchange(t, a, confirmationFrame(hello, prepared.Intent, 3, "44444444444444444444444444444444"), 22)
	if approved.Approval == nil || approved.Approval.ApprovalRequestID != "44444444444444444444444444444444" || s.issuer.Snapshot().Generation != 1 || s.issuer.Snapshot().HasAuthority {
		t.Fatal("fresh consent not bound to new owner")
	}
}
func TestSIDBindingFactCannotTransferAcrossServices(t *testing.T) {
	_, a := sidConfirmationFixture(t)
	sidExchange(t, a, dispatchHello, 11)
	_, other := sidConfirmationFixture(t)
	other.operator = a.operator // private fault model; no public setter exists
	b, _ := sidBatch(other, dispatchHello, 11)
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := other.exchangeOps(ctx, b, func(context.Context, []byte) error { t.Error("foreign owner binding wrote reply"); return nil }); err == nil {
		t.Fatal("same principal fact transferred to another owner/issuer")
	}
}
func TestSIDLateConsumedSubjectCannotSurviveStopOrCancellation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "stop"}[stop], func(t *testing.T) {
			s, a := sidConfirmationFixture(t)
			b, p := sidBatch(a, dispatchHello, 11)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			b.consumeAuthenticated = func(context.Context, *operatorauth.Read) ([]byte, serviceOperatorSubject, error) {
				close(entered)
				<-release
				return []byte(dispatchHello), p, nil
			}
			var closes atomic.Int32
			b.close = func(context.Context) error { closes.Add(1); return nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, end := context.WithTimeout(ctx, 2*time.Second)
			defer end()
			done := make(chan error, 1)
			go func() {
				done <- a.exchangeOps(request, b, func(context.Context, []byte) error { t.Error("late subject wrote reply"); return nil })
			}()
			select {
			case <-entered:
			case <-request.Done():
				t.Fatal("consume did not enter")
			}
			if stop {
				s.stopFor(ErrProvisioningRejected)
			} else {
				cancel()
			}
			if a.operator != nil || s.Snapshot().CleanupConfirmed {
				t.Fatal("pending consume was adopted or reclaimed")
			}
			unblock()
			cleanup, finish := context.WithTimeout(context.Background(), 2*time.Second)
			defer finish()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("late subject accepted")
				}
			case <-cleanup.Done():
				t.Fatal("late owner did not return")
			}
			if a.operator != nil || closes.Load() != 1 || !s.issuer.Snapshot().Closed {
				t.Fatal("late return lost revocation/owned cleanup")
			}
		})
	}
}
