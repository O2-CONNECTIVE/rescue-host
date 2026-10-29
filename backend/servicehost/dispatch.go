package servicehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// native認証providerと保護profileの供給は未接続。private constructorを
// 外部入力や自己申告boolから呼ぶ公開factoryは提供しない。
type serviceDispatchProfile struct {
	ChannelNonce string
	RequestLimit uint64
}
type authenticatedReadTicket struct{ marker byte }

// exact readの単回capability。nonce/digest一致自体を認証の根拠にはしない。
// 生成は同owner adapterの責務。concrete Batchの供給元は未接続のまま。
type authenticatedServiceRead struct {
	owner  *serviceDispatcher
	frame  [32]byte
	ticket *authenticatedReadTicket
}
type serviceReadVerifier interface {
	verify(context.Context, []byte) (authenticatedServiceRead, error)
}
type serviceDispatcher struct {
	mu            sync.Mutex
	service       *Service
	profile       serviceDispatchProfile
	verifier      serviceReadVerifier
	hello, closed bool
	pending       *serviceDispatchReply
	issuer        *ephemeral.Issuer
	boot          string
	sequence      uint64
	ids           map[string]struct{}
	reads         map[*authenticatedReadTicket]struct{}
}
type serviceDispatchReply struct {
	owner *serviceDispatcher
	frame []byte
	ctx   context.Context
	hello bool
}

func newServiceDispatcher(s *Service, p serviceDispatchProfile, v serviceReadVerifier) (*serviceDispatcher, error) {
	if s == nil || v == nil || p.RequestLimit == 0 || p.RequestLimit > 1024 || !dispatchHex(p.ChannelNonce, 64) {
		return nil, ErrProvisioningUnconfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used || s.started || s.stopping || s.dispatch != nil || (s.prepare == nil && s.control == nil) || s.done == nil {
		return nil, ErrProvisioningRejected
	}
	d := &serviceDispatcher{service: s, profile: p, verifier: v, ids: make(map[string]struct{}), reads: make(map[*authenticatedReadTicket]struct{})}
	s.dispatch = d
	return d, nil
}
func dispatchHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (d *serviceDispatcher) begin(ctx context.Context, frame []byte) (*serviceDispatchReply, error) {
	if d == nil {
		return nil, ErrProvisioningRejected
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.pending != nil || !controlBudget(ctx) || len(frame) == 0 || len(frame) > servicecontrol.FrameBytes {
		return d.rejectLocked()
	}
	frame = append([]byte(nil), frame...)
	digest := sha256.Sum256(frame)
	proof, err := verifyServiceRead(ctx, d.verifier, frame)
	if err != nil || ctx.Err() != nil || proof.owner != d || proof.ticket == nil || proof.frame != digest || sha256.Sum256(frame) != digest {
		return d.rejectLocked()
	}
	if _, used := d.reads[proof.ticket]; used {
		return d.rejectLocked()
	}
	d.reads[proof.ticket] = struct{}{}
	request, err := servicecontrol.DecodeUntrustedFrame(frame)
	if err != nil {
		return d.rejectLocked()
	}
	s := d.service
	s.mu.Lock()
	if s.dispatch != d || s.issuer == nil || (!d.hello && (s.state != Running || s.stopping || !s.authWriter.OpenLocked())) || (d.hello && s.issuer != d.issuer) {
		s.mu.Unlock()
		return d.rejectLocked()
	}
	issuer := s.issuer
	phase, stopping := s.state, s.stopping
	unprojected := s.ownerIntent != nil || s.reservation != nil
	owner := issuer.Snapshot()
	s.mu.Unlock()
	if owner.Generation != 0 || owner.InstanceEpoch == ([32]byte{}) {
		return d.rejectLocked()
	}
	boot := hex.EncodeToString(owner.InstanceEpoch[:])
	response := servicecontrol.UntrustedResponse{BootEpoch: boot, ChannelNonce: d.profile.ChannelNonce, RequestID: request.RequestID, Sequence: request.Sequence, Command: request.Command}
	if !d.hello {
		if request.Type != servicecontrol.Hello || owner.Closed {
			return d.rejectLocked()
		}
		d.issuer = issuer
		d.boot = boot
		response.Type = "hello"
		response.ClientNonce = request.ClientNonce
		response.RequestLimit = d.profile.RequestLimit
	} else {
		if request.Type != servicecontrol.Request || request.BootEpoch != d.boot || request.ChannelNonce != d.profile.ChannelNonce || request.Sequence != d.sequence+1 || d.sequence >= d.profile.RequestLimit {
			return d.rejectLocked()
		}
		if _, used := d.ids[request.RequestID]; used {
			return d.rejectLocked()
		}
		response.Type = "response"
		if request.Command == servicecontrol.Status && unprojected {
			// 所有ticketを承認ID/binding/launchPhaseへ合成するproducerは未接続。
			response.Result = "error"
			response.Error = &servicecontrol.UntrustedServiceError{Code: "SERVICE_NOT_READY"}
		} else if request.Command == servicecontrol.Status {
			response.Result = "snapshot"
			response.Snapshot = &servicecontrol.UntrustedSnapshot{ServicePhase: string(phase), LaunchPhase: "none"}
			if stopping {
				response.Snapshot.LaunchPhase = "quiescing"
			}
		} else if request.Command == servicecontrol.RevokeAllAndQuiesce {
			receipt, err := s.dispatchQuiesce(d)
			if err != nil || !receipt.matches(d) {
				_, _ = d.rejectLocked()
				return nil, ErrRuntimeUnconfirmed
			}
			response.Result = "quiesce-accepted"
			response.Quiesce = &servicecontrol.UntrustedQuiesce{RevocationApplied: true}
		} else {
			response.Result = "error"
			response.Error = &servicecontrol.UntrustedServiceError{Code: "SERVICE_UNCONFIGURED"}
		}
		d.sequence = request.Sequence
	}
	d.ids[request.RequestID] = struct{}{}
	encoded, err := servicecontrol.EncodeResponse(response)
	if err != nil {
		return d.rejectLocked()
	}
	reply := &serviceDispatchReply{owner: d, frame: encoded, ctx: ctx, hello: request.Type == servicecontrol.Hello}
	d.pending = reply
	return reply, nil
}

// 呼出側は有限な単一writeの実completionを一度だけ通知する。未完了の間は
// 次の要求を処理せず、失敗を成功扱いにして再送・同一ID再利用しない。
func (d *serviceDispatcher) complete(reply *serviceDispatchReply, writeErr error) error {
	if d == nil {
		return ErrProvisioningRejected
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if reply == nil || reply.owner != d || d.pending != reply || d.closed {
		_, _ = d.rejectLocked()
		return ErrProvisioningRejected
	}
	if writeErr != nil || reply.ctx == nil || reply.ctx.Err() != nil {
		_, _ = d.rejectLocked()
		return ErrRuntimeUnconfirmed
	}
	d.pending = nil
	if reply.hello {
		d.hello = true
	}
	if d.sequence == d.profile.RequestLimit {
		_, _ = d.rejectLocked()
	}
	return nil
}

// verifierは現在frameのnative認証完了を返すprivate seam。panic/取消/復元失敗は
// 一つの拒否へ落とし、内部errorやpayloadをresponseへ転送しない。
func verifyServiceRead(ctx context.Context, v serviceReadVerifier, frame []byte) (proof authenticatedServiceRead, err error) {
	defer func() {
		if recover() != nil {
			proof = authenticatedServiceRead{}
			err = ErrProvisioningRejected
		}
	}()
	return v.verify(ctx, frame)
}
func (d *serviceDispatcher) rejectLocked() (*serviceDispatchReply, error) {
	d.closed = true
	d.pending = nil
	if d.issuer != nil {
		_, _ = d.service.dispatchQuiesce(d)
	}
	return nil, ErrProvisioningRejected
}
