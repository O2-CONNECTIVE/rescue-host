package servicehost

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// このprivate receiptは同じownerによる実issuer.Closeの成功からだけ作る。
// wire値・Snapshot・#100のSCM cleanup proofからは作らない。
type serviceRevocationReceipt struct {
	owner  *Service
	issuer *ephemeral.Issuer
}

func (s *Service) dispatchQuiesce(d *serviceDispatcher) (serviceRevocationReceipt, error) {
	s.mu.Lock()
	issuer := s.issuer
	matched := s.dispatch == d && issuer != nil && issuer == d.issuer
	s.mu.Unlock()
	if !matched {
		return serviceRevocationReceipt{}, ErrProvisioningRejected
	}
	s.stopFor(nil)
	// stopForの戻り値は既停止の観測であり、receiptに転用しない。
	if err := issuer.Close(); err != nil {
		return serviceRevocationReceipt{}, ErrRuntimeUnconfirmed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispatch != d || s.issuer != issuer || !s.stopping || fatalServiceThread(s.stopCause) {
		return serviceRevocationReceipt{}, ErrRuntimeUnconfirmed
	}
	return serviceRevocationReceipt{owner: s, issuer: issuer}, nil
}
func (r serviceRevocationReceipt) matches(d *serviceDispatcher) bool {
	return d != nil && r.owner == d.service && r.issuer != nil && r.issuer == d.issuer
}
