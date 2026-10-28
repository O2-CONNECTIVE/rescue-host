package servicehost_test

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

type issuedObserver struct {
	legacy, issued int
	context        servicehost.IssuedLaunch
	authority      *supervisor.Authority
}

func (o *issuedObserver) Start(context.Context, *supervisor.Authority) (servicehost.Process, error) {
	o.legacy++
	return nil, errors.New("context未提供")
}
func (o *issuedObserver) StartIssued(_ context.Context, a *supervisor.Authority, i servicehost.IssuedLaunch) (servicehost.Process, error) {
	o.issued++
	o.context = i
	o.authority = a
	return &processAPI{wait: func(context.Context) error { return errors.New("合成された退出観測") }, close: func(context.Context) error { return nil }}, nil
}

func TestHostSuppliesCurrentIssuerContextWithSameAuthority(t *testing.T) {
	observer := &issuedObserver{}
	h, err := servicehost.WithRuntime(approvedPolicyFunc(func(context.Context) (ephemeral.ApprovedPolicy, error) { return provisionedPolicy(), nil }), observer, servicehost.Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Run(context.Background())
	if observer.issued != 1 || observer.legacy != 0 || observer.authority == nil {
		t.Fatal("現在issuerのtyped contextをlauncherへ渡していない")
	}
	grant := observer.authority.Snapshot(time.Now()).Grant
	epoch := observer.context.InstanceEpoch()
	if epoch == ([32]byte{}) || observer.context.Binding() != grant.Binding || !strings.HasPrefix(grant.Binding.Identity.LaunchID, hex.EncodeToString(epoch[:])+"-") {
		t.Fatal("別issuerまたは別Authorityの相関値")
	}
	epoch[0]++
	if observer.context.InstanceEpoch() == epoch {
		t.Fatal("callerのcopy変更でissuer相関値が変わった")
	}
}
