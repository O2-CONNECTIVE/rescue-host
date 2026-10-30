package servicehost

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// This operation seam is private. Public composition always selects the concrete
// endpoint checks and childjob observations; tests model only those operations.
type runtimeGuestKernel interface {
	inspection(*serviceAuthRequest) *serviceready.InspectionLease // Service.mu; no native call
	material(*childjob.Prepared, childjob.GuestTokenExpectation) runtimeGuestTarget
}
type runtimeGuestTarget interface {
	confirmationTargetOps
	matchesGuestSID(string) bool
}
type runtimeGuestMaterial struct {
	prepared *childjob.Prepared // retained even when no usable target was returned
	target   runtimeGuestTarget
}
type runtimeGuestNativeKernel struct{}

func (runtimeGuestNativeKernel) inspection(r *serviceAuthRequest) *serviceready.InspectionLease {
	if r == nil || !r.authenticatedEndpointLocked() {
		return nil
	}
	return r.endpoint.inspection
}
func (runtimeGuestNativeKernel) material(p *childjob.Prepared, e childjob.GuestTokenExpectation) runtimeGuestTarget {
	return &preparedConfirmationTarget{prepared: p, expectation: &e, requireProtected: true}
}

func runtimeGuestMaterialFor(k runtimeGuestKernel, p *childjob.Prepared, e childjob.GuestTokenExpectation) (m *runtimeGuestMaterial, err error) {
	m = &runtimeGuestMaterial{prepared: p}
	defer func() {
		if recover() != nil {
			err = ErrRuntimeUnconfirmed
		}
	}()
	m.target = k.material(p, e)
	if m.target == nil {
		err = ErrProvisioningRejected
	}
	return
}
