package servicehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func TestConfirmedGrantRejectsForeignCopiedStaleOrIncompleteRuntimeProof(t *testing.T) {
	for _, kind := range []string{"missing", "copy", "unheld", "owner-self", "owner-source", "owner-service", "owner-issuer", "owner-auth", "owner-outer", "source", "owner", "issuer", "auth-owner", "outer", "confirmation", "config", "policy", "keyset", "keys", "deadline", "lease", "boundary", "staging-sid", "runtime-sid", "expected-sid-empty", "expected-sid-malformed", "initial-profile-sid", "consumed"} {
		t.Run(kind, func(t *testing.T) {
			var s *Service
			s, a, _ := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
				_, err := ticket.PreviewFor(s)
				return err
			})
			source := grantRuntimeFixture(t, s)
			kernel := source.kernel
			var partial *confirmationRuntimeProof
			source.kernel = runtimeKernelModel(func(ctx context.Context, ticket PreviewConfirmationTicket) (*confirmationRuntimeProof, error) {
				p, err := kernel.Acquire(ctx, ticket)
				if err != nil {
					return p, err
				}
				switch kind {
				case "missing":
					p = nil
				case "copy":
					copied := *p
					p = &copied
				case "unheld":
					delete(source.owner.proofs, p)
				case "source":
					p.source = &confirmationRuntimeSource{}
				case "owner":
					p.owner = &confirmationEvidenceOwner{}
				case "owner-self":
					source.owner.self = &confirmationEvidenceOwner{}
				case "owner-source":
					source.owner.source = &confirmationRuntimeSource{}
				case "owner-service":
					source.owner.service = &Service{}
				case "owner-issuer":
					source.owner.issuer, err = ephemeral.New()
					if err != nil {
						t.Error(err)
					}
				case "owner-auth":
					source.owner.auth = nil
				case "owner-outer":
					source.owner.outer = &innerCleanupTicket{}
				case "issuer":
					p.issuer, err = ephemeral.New()
					if err != nil {
						t.Error(err)
					}
				case "auth-owner":
					p.auth = nil
				case "outer":
					p.outer = &innerCleanupTicket{}
				case "confirmation":
					copied := *p.confirmation
					p.confirmation = &copied
				case "config":
					p.descriptor.ConfigDigest[0] ^= 1
				case "policy":
					p.descriptor.PolicyDigest[0] ^= 1
				case "keyset":
					p.descriptor.KeySetDigest[0] ^= 1
				case "keys":
					p.descriptor.AllowedKeyFingerprints[0] = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
				case "deadline":
					p.descriptor.HardDeadline = p.descriptor.HardDeadline.Add(time.Nanosecond)
				case "lease":
					p.descriptor.LeaseCeiling += time.Nanosecond
				case "boundary":
					p.boundary = 0 // staging alone is never runtime inspection
				case "staging-sid":
					p.stagingSID = "S-1-5-21-7-8-9-2049"
				case "runtime-sid":
					p.runtimeSID = "S-1-5-21-7-8-9-2049"
				case "expected-sid-empty":
					source.owner.expectedSID = ""
				case "expected-sid-malformed":
					source.owner.expectedSID = "S-1-5-21-07-8-9-2048"
				case "initial-profile-sid":
					source.owner.expectedSID = s.preview.excludedSIDs[3]
					p.stagingSID, p.runtimeSID = source.owner.expectedSID, source.owner.expectedSID
				case "consumed":
					p.consumed = true
				}
				partial = p
				return p, nil
			})
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			writes := 0
			err := a.exchangeOps(ctx, fixtureAuthBatch(a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333")), func(context.Context, []byte) error { writes++; return nil })
			if err == nil || writes != 0 || s.issuer.Snapshot().Generation != 0 || !s.issuer.Snapshot().Closed || s.Snapshot().HasAuthority {
				t.Fatal("invalid runtime proof issued or survived canonical revoke", err)
			}
			s.mu.Lock()
			r := s.preview.intent.confirmation
			retained := r.runtimeProof == partial && r.runtimeOwner == source.owner && (partial == nil || r.partialOwner == partial.owner)
			finished := r.finished && s.reservation.finished && s.reservation.claimed && s.ownerIntent.burned
			s.mu.Unlock()
			if !retained || !finished {
				t.Fatal("failed proof lost partial ownership or actual reservation finish")
			}
		})
	}
}

func TestConfirmedGrantCannotReissueAfterWriteLossReconnectOrIndependentRevoke(t *testing.T) {
	for _, kind := range []string{"write-loss", "reconnect", "reprepare", "reconfirm", "owner-revoke", "unheld-generation"} {
		t.Run(kind, func(t *testing.T) {
			var s *Service
			s, a, _ := confirmationFixture(t, func(_ context.Context, ticket PreviewConfirmationTicket) error {
				_, err := ticket.PreviewFor(s)
				return err
			})
			source := grantRuntimeFixture(t, s)
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			frame := confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			writes := 0
			err := a.exchangeOps(ctx, fixtureAuthBatch(a, frame), func(context.Context, []byte) error {
				writes++
				if kind == "write-loss" {
					return errors.New("model lost write completion")
				}
				return nil
			})
			if kind != "write-loss" && err != nil || writes != 1 {
				t.Fatal("initial grant did not reach its exact write", err)
			}
			switch kind {
			case "write-loss":
				if err == nil {
					t.Fatal("lost write was acknowledged")
				}
			case "reconnect":
				frame = dispatchHello
			case "reprepare":
				frame = dispatchRequest(hello, 3, "44444444444444444444444444444444", servicecontrol.PrepareIntent)
			case "reconfirm":
				frame = confirmationFrame(hello, prepared.Intent, 3, "44444444444444444444444444444444")
			case "owner-revoke":
				if err := source.owner.revoke(); err != nil {
					t.Fatal(err)
				}
				frame = dispatchRequest(hello, 3, "44444444444444444444444444444444", servicecontrol.Status)
			case "unheld-generation":
				if err := s.issuer.Revoke(s.preview.grant.binding); err != nil {
					t.Fatal(err)
				}
				if _, err := s.issuer.Prepare(ephemeral.ApprovedPolicy{}); err != nil {
					t.Fatal(err)
				} // pure core foreign/unheld pending entry
				frame = dispatchRequest(hello, 3, "44444444444444444444444444444444", servicecontrol.Status)
			}
			if err := a.exchangeOps(ctx, fixtureAuthBatch(a, frame), func(context.Context, []byte) error { writes++; return nil }); err == nil {
				t.Fatal("lost/consumed/foreign owner channel admitted another request")
			}
			wantGeneration := uint64(1)
			if kind == "unheld-generation" {
				wantGeneration = 2
			}
			if actual := s.issuer.Snapshot(); actual.Generation != wantGeneration || !actual.Closed || actual.HasAuthority || writes != 1 {
				t.Fatal("replay reissued or left canonical issuer open", actual)
			}
		})
	}
}
