package servicehost

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

func TestConfirmedGrantHelperCompletionIsReusedOnlyAfterItsExactConsume(t *testing.T) {
	for _, kind := range []string{"grant-consumed", "legacy-consumed", "before-consume"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { return ErrRuntimeUnconfirmed })
			if kind != "legacy-consumed" {
				grantRuntimeFixture(t, s)
			}
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			batch := fixtureAuthBatch(a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
			collect, consume := batch.collect, batch.consume
			// Existing private auth OS seam models Collect's real wait/IO/process
			// close acknowledgement. This flag never enters production authority.
			helperClosed, consumed := false, false
			batch.collect = func(ctx context.Context) (*operatorauth.Read, error) {
				if kind == "before-consume" {
					return nil, ErrRuntimeUnconfirmed
				}
				proof, err := collect(ctx)
				helperClosed = err == nil && proof != nil
				return proof, err
			}
			batch.consume = func(ctx context.Context, proof *operatorauth.Read) ([]byte, error) {
				if !helperClosed {
					t.Error("Consume preceded helper close acknowledgement")
				}
				frame, err := consume(ctx, proof)
				consumed = err == nil
				return frame, err
			}
			closes := 0
			batch.close = func(ctx context.Context) error { closes++; return s.authOwner.AwaitWorkload(ctx) }
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := a.exchangeOps(ctx, batch, func(context.Context, []byte) error { t.Error("failed confirmation wrote a response"); return nil }); err == nil {
				t.Fatal("failed confirmation succeeded")
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			want := 1
			if kind == "grant-consumed" {
				want = 0
			}
			if closes != want || consumed != (kind != "before-consume") {
				t.Fatal("wrong helper lifecycle acknowledgement reused", closes, consumed)
			}
		})
	}
}
