package operatorauth

import (
	"context"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// OperatorBinding は同じhelper/launchの認証済み実主体と消費来歴の不変な記録。
// 権限、admission、接続の生存、失効/停止receiptではない。外部から構築できない。
type OperatorBinding struct {
	self      *OperatorBinding
	collector *Collector
	owner     *serviceauthowner.Owner
	issuer    *ephemeral.Issuer
	subject   tokenProjection
	policy    SIDPolicy
}

func (b *OperatorBinding) valid() bool {
	return b != nil && b.self == b && b.collector != nil && b.owner != nil && b.issuer != nil && b.policy.accepts(b.subject)
}

// Matches は消費時の同Collector/canonical owner/issuerという不変の来歴だけ。
// callerは現在の要求・channel・停止critical sectionのadmissionを別途検査する。
func (b *OperatorBinding) Matches(c *Collector, owner *serviceauthowner.Owner, issuer *ephemeral.Issuer) bool {
	return b.valid() && c != nil && b.collector == c && owner != nil && b.owner == owner && issuer != nil && b.issuer == issuer && c.authOwner == owner && c.authIssuer == issuer
}

// SameSubject は認証済みの全projectionを比較する。古い要求の取消は事実を消さない。
// 同SIDでもAuthId/session/integrity/kind/level等が違えば同じchannelへ引き継がない。
func (b *OperatorBinding) SameSubject(other *OperatorBinding) bool {
	return b.valid() && other.valid() && b.subject == other.subject
}

// ConsumeAuthenticated はframeとsealed subjectを同時に単回消費する方式B専用入口。
// 別Collectorのreadへ触れず、本人の取消/期限/回収競合はその消費権をburnする。
func (c *Collector) ConsumeAuthenticated(ctx context.Context, r *Read) ([]byte, *OperatorBinding, error) {
	if c == nil || r == nil {
		return nil, nil, ErrDenied
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.owner != c || c.proof != r || r.used || c.sidPolicy == (SIDPolicy{}) {
		return nil, nil, ErrDenied
	}
	r.used = true
	if !c.sidPolicy.accepts(r.subject) || c.authOwner == nil || !c.authOwner.MatchesIssuer(c.authIssuer) || c.failed || c.closed || (c.request != nil && !reportContextValid(c.request)) || !reportContextValid(ctx) || !reportContextValid(r.request) || !time.Now().Before(r.deadline) {
		return nil, nil, ErrDenied
	}
	binding := &OperatorBinding{collector: c, owner: c.authOwner, issuer: c.authIssuer, subject: r.subject, policy: c.sidPolicy}
	binding.self = binding
	return append([]byte(nil), r.frame...), binding, nil
}

// MatchesSIDPolicy は元の保護policyと実認証主体の不変の対応だけを比較する。
// 値一致は現在のadmissionやRoot配置保護の証明を代用しない。
func (b *OperatorBinding) MatchesSIDPolicy(operatorSID, guestSID string) bool {
	return b.valid() && b.policy.OperatorSID == operatorSID && b.policy.GuestSID == guestSID
}
