package servicehost

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"time"

	backend "github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// PreviewDescriptor contains display candidates only. It proves neither protected
// provenance, operator authentication, approval, nor authority to launch.
type PreviewDescriptor struct {
	ConfigDigest, PolicyDigest, KeySetDigest [32]byte
	AllowedKeyFingerprints                   []string
	PolicyMode                               string
	HardDeadline                             time.Time
	LeaseCeiling                             time.Duration
}
type PreviewServiceConfig struct {
	Ready                     serviceready.Config
	Budgets                   Budgets
	RequestLimit, LaunchLimit uint64
	IntentTTL                 time.Duration
	Preview                   PreviewDescriptor
}
type servicePreview struct {
	descriptor PreviewDescriptor
	ttl        time.Duration
	intent     *previewIntent // Service.mu; distinct from workload serviceOwnerIntent
	seen       map[string]struct{}
}
type previewIntent struct {
	owner       *Service
	dispatcher  *serviceDispatcher
	id          string
	expires     time.Time
	invalidated bool
}

// NewPreviewService builds only the concrete query/auth composition. Descriptor
// validity is not trusted provisioning, and confirm-approval stays unconfigured.
// No successful legacy Approval/Launcher substitute is installed.
func NewPreviewService(c PreviewServiceConfig) (*Service, error) {
	preview, err := newServicePreview(c.Preview, c.IntentTTL, time.Now())
	if err != nil {
		return nil, err
	}
	if c.RequestLimit == 0 || c.RequestLimit >= c.LaunchLimit || c.LaunchLimit > 1024 {
		return nil, ErrProvisioningUnconfigured
	}
	service, err := allocateService(ServiceConfig{Budgets: c.Budgets}, nil)
	if err != nil {
		return nil, err
	}
	factories, err := newServiceNativeFactories(c.Ready)
	if err != nil {
		return nil, err
	}
	nonce, err := previewRandomHex(32)
	if err != nil {
		return nil, err
	}
	service.preview = preview
	service.serviceName = c.Ready.Profile.ServiceName
	if err := bindServiceControl(service, factories, serviceDispatchProfile{ChannelNonce: nonce, RequestLimit: c.RequestLimit}, c.LaunchLimit); err != nil {
		return nil, err
	}
	return service, nil
}
func newServicePreview(descriptor PreviewDescriptor, ttl time.Duration, now time.Time) (*servicePreview, error) {
	policy, err := backend.PolicyDigest(descriptor.PolicyMode)
	if err != nil || policy != hex.EncodeToString(descriptor.PolicyDigest[:]) || descriptor.ConfigDigest == ([32]byte{}) || descriptor.PolicyDigest == ([32]byte{}) || descriptor.KeySetDigest == ([32]byte{}) || ttl <= 0 || descriptor.LeaseCeiling <= 0 || descriptor.LeaseCeiling > time.Minute || !descriptor.HardDeadline.After(now) || descriptor.HardDeadline.UnixMilli() <= now.UnixMilli() || descriptor.HardDeadline.UnixMilli() <= 0 {
		return nil, ErrProvisioningUnconfigured
	}
	descriptor.AllowedKeyFingerprints = append([]string(nil), descriptor.AllowedKeyFingerprints...)
	// Reuse existing wire vocabulary and canonical fingerprint validation. These
	// fixed correlation placeholders validate shape only and are never issued.
	const zero64 = "0000000000000000000000000000000000000000000000000000000000000000"
	const zero32 = "00000000000000000000000000000000"
	if _, err := servicecontrol.CanonicalPreview(previewWire(descriptor, zero64, zero32)); err != nil {
		return nil, ErrProvisioningUnconfigured
	}
	return &servicePreview{descriptor: descriptor, ttl: ttl, seen: make(map[string]struct{})}, nil
}
func previewWire(descriptor PreviewDescriptor, boot, id string) servicecontrol.UntrustedPreview {
	return servicecontrol.UntrustedPreview{BootEpoch: boot, IntentID: id, ConfigDigest: hex.EncodeToString(descriptor.ConfigDigest[:]), PolicyDigest: hex.EncodeToString(descriptor.PolicyDigest[:]), KeySetDigest: hex.EncodeToString(descriptor.KeySetDigest[:]), AllowedKeyFingerprints: append([]string(nil), descriptor.AllowedKeyFingerprints...), Target: servicecontrol.UntrustedPreviewTarget{Host: "127.0.0.1", Port: "22", OuterChannel: "direct-tcpip"}, PermissionSummary: servicecontrol.UntrustedPermissionSummary{PolicyMode: descriptor.PolicyMode, GuestClass: "nonadmin", OuterChannels: []string{"direct-tcpip"}}, HardDeadlineUnixMs: descriptor.HardDeadline.UnixMilli()}
}
func previewRandomHex(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", ErrRuntimeUnconfirmed
	}
	return hex.EncodeToString(raw), nil
}
