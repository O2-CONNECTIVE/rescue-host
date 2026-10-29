package servicehost

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func TestControlLegacyReplyRequiresExactPendingReplyIdentity(t *testing.T) {
	s, a, _ := authTestService(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	writes := 0
	if err := a.exchangeOps(ctx, fixtureAuthBatch(a, dispatchHello), func(context.Context, []byte) error {
		s.mu.Lock()
		r := a.active
		reply := r.reply
		s.mu.Unlock()
		if reply == nil {
			t.Error("actual dispatcher reply was not retained")
			return ErrProvisioningRejected
		}
		copied := *reply
		if err := r.writeDispatchReply(&copied, func(context.Context, []byte) error { writes++; return nil }); err == nil {
			t.Error("copied reply reached writer")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatal("foreign reply was physically written")
	}
}

func TestControlEndpointWriteRefusesUnconfiguredInputWithoutMintingReceipt(t *testing.T) {
	_, a := authUnstartedMemoryOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	if err := r.runOwner(func() error {
		if _, err := r.prepareEndpointOps(r.worker, func(context.Context) (serviceAuthEndpointOps, error) {
			return serviceAuthEndpointOps{concrete: &serviceready.Endpoint{}, close: func(context.Context) error { return nil }}, nil
		}); err != nil {
			return err
		}
		return r.writeDispatchReply(nil, func(context.Context, []byte) error { writes++; return nil })
	}); err == nil {
		t.Fatal("unconfigured input reached physical response path")
	}
	if writes != 0 || r.endpoint.written != nil {
		t.Fatal("unconfigured input created write evidence")
	}
}

func TestControlEndpointPreparationRequiresTheOwnedFactoryPair(t *testing.T) {
	_, a, _ := controlReadinessFixture(t, func(context.Context) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := a.reserveExchange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.runOwner(func() error { _, err := r.prepareControlEndpoint(r.worker); return err }); err == nil {
		t.Fatal("fixture readiness substituted for the concrete factory pair")
	}
	if r.endpoint != nil {
		t.Fatal("missing concrete pair reached endpoint acquisition")
	}
}
