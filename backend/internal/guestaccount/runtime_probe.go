package guestaccount

import (
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

// The runtime candidate has one fixed harmless workload. CleanupTimeout is only
// a cleanup budget; it never creates or extends the execution admission lease.
func runtimeProbeSpec(layout *FixedLayout, cleanup time.Duration) (childjob.Spec, error) {
	if layout == nil || !layout.configured || !layout.runtime || cleanup <= 0 {
		return childjob.Spec{}, ErrUnconfigured
	}
	p := layout.profile.native
	command, err := childjob.NewFixedCommand([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "exit 0"}, []childjob.Artifact{p.manifest, p.assembly})
	if err != nil {
		return childjob.Spec{}, err
	}
	return childjob.Spec{Artifact: p.image, Command: command, CleanupTimeout: cleanup}, nil
}
