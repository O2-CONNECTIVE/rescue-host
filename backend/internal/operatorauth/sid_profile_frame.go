package operatorauth

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// profile/2は期待AuthId/session/integrityの欄自体を持たない方式B専用型。
// frameのdecode成功は保護sourceや同launchの来歴証明ではない。
func encodeSIDProfileFrame(launchID string, p helperProfile) ([]byte, error) {
	if !p.sid.valid() || !profileLaunchID(launchID) || p.launchID != launchID || p.operator != (tokenProjection{}) || p.guest != (tokenProjection{}) {
		return nil, ErrDenied
	}
	w := struct {
		Version  string `json:"version"`
		Type     string `json:"type"`
		Mode     string `json:"mode"`
		LaunchID string `json:"launchId"`
		Operator string `json:"operatorSID"`
		Guest    string `json:"guestUser"`
	}{"2", "helper-profile", "sid-message", launchID, p.sid.OperatorSID, p.sid.GuestSID}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, ErrDenied
	}
	return append(b, '\n'), nil
}
func decodeSIDProfileObject(root map[string]any, expected string) (helperProfile, error) {
	if !profileLaunchID(expected) || !profileKeys(root, "version", "type", "mode", "launchId", "operatorSID", "guestUser") || root["version"] != "2" || root["type"] != "helper-profile" || root["mode"] != "sid-message" || root["launchId"] != expected {
		return helperProfile{}, ErrDenied
	}
	operator, ok := root["operatorSID"].(string)
	if !ok {
		return helperProfile{}, ErrDenied
	}
	guest, ok := root["guestUser"].(string)
	if !ok {
		return helperProfile{}, ErrDenied
	}
	p := SIDPolicy{OperatorSID: operator, GuestSID: guest}
	if !p.valid() {
		return helperProfile{}, ErrDenied
	}
	return helperProfile{sid: p, launchID: expected}, nil
}

// lifetime producerは正規policyを保持し、候補process/tokenから期待値を作らない。
// これ自体は専用helper tokenやOS launch evidenceを供給しない。
type profileLifetimeProducer interface {
	profilePipeProducer
	bindRequest(context.Context) error
	recheck(context.Context) error
	close(context.Context) error
}
type sidProfileProvider struct {
	mu           sync.Mutex
	material     profilePipeMaterial
	request      context.Context
	used, closed bool
}

func newSIDProfileProvider(launchID string, policy SIDPolicy, cleanup time.Duration) (*sidProfileProvider, error) {
	p := helperProfile{sid: policy, launchID: launchID}
	if _, err := encodeSIDProfileFrame(launchID, p); err != nil || cleanup <= 0 || cleanup > time.Minute {
		return nil, ErrUnconfigured
	}
	return &sidProfileProvider{material: profilePipeMaterial{launchID: launchID, profile: p, cleanup: cleanup}}, nil
}
func (p *sidProfileProvider) bindRequest(ctx context.Context) error {
	if p == nil || !p.mu.TryLock() {
		return ErrDenied
	}
	defer p.mu.Unlock()
	if p.used || p.closed || p.request != nil || !reportContextValid(ctx) {
		return ErrDenied
	}
	p.request = ctx
	return nil
}
func (p *sidProfileProvider) prepare(ctx context.Context) (profilePipeMaterial, error) {
	if p == nil || !p.mu.TryLock() {
		return profilePipeMaterial{}, ErrDenied
	}
	defer p.mu.Unlock()
	if p.used || p.closed || !reportContextValid(ctx) || !reportContextValid(p.request) {
		return profilePipeMaterial{}, ErrDenied
	}
	p.used = true
	return p.material, nil
}
func (p *sidProfileProvider) recheck(ctx context.Context) error {
	if p == nil || !p.mu.TryLock() {
		return ErrDenied
	}
	defer p.mu.Unlock()
	if p.closed || !reportContextValid(ctx) || !reportContextValid(p.request) {
		return ErrDenied
	}
	return nil
}
func (p *sidProfileProvider) close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if !p.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer p.mu.Unlock()
	p.closed = true
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
