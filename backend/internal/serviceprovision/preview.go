package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"golang.org/x/crypto/ssh"
)

// PreviewLimitsは保護呼出側の有限予算で、wire/環境変数から選ばない。
// 表示用intentの期限を指定するだけで、承認・grant・起動権限を与えない。
type PreviewLimits struct {
	RequestLimit, LaunchLimit uint64
	IntentTTL                 time.Duration
}

// PreviewDescriptorは同じ元threadの保持Recordから表示候補を解決する。
// 戻り値は認証・protected provenance・人の承認を表すcapabilityではない。
func (p *Prepared) PreviewDescriptor(ctx context.Context) (servicehost.PreviewDescriptor, error) {
	preview, _, _, err := p.previewRecord(ctx)
	return preview, err
}

// ComposePreviewServiceはquery-only tokenを借用し、承認/launcher無しの明示的な
// preview経路へ接続する。既存legacy構成や実機mainの未設定拒否は変更しない。
func (p *Prepared) ComposePreviewService(ctx context.Context, limits PreviewLimits) (*servicehost.Service, error) {
	service, _, err := p.composePreviewSnapshot(ctx, limits)
	return service, err
}

// ownerは構成に使ったexact snapshotを保持する。descriptor自体は承認ではない。
func (p *Prepared) composePreviewSnapshot(ctx context.Context, limits PreviewLimits) (*servicehost.Service, servicehost.PreviewDescriptor, error) {
	preview, r, token, err := p.previewRecord(ctx)
	if err != nil {
		return nil, servicehost.PreviewDescriptor{}, err
	}
	service, err := servicehost.NewPreviewService(servicehost.PreviewServiceConfig{
		Ready:   serviceready.Config{Profile: r.Profile, PrimaryToken: token, Endpoint: r.Endpoint, ServiceCommandLine: r.ServiceCommandLine, ServiceSecurity: r.ServiceSecurity, ProcessSecurity: r.ProcessSecurity, CleanupTimeout: r.ReadyCleanupTimeout},
		Budgets: r.Budgets, RequestLimit: limits.RequestLimit, LaunchLimit: limits.LaunchLimit, IntentTTL: limits.IntentTTL, Preview: preview,
	})
	if err != nil {
		return nil, servicehost.PreviewDescriptor{}, err
	}
	return service, preview, nil
}

func (p *Prepared) previewRecord(ctx context.Context) (servicehost.PreviewDescriptor, Record, uintptr, error) {
	reject := func(err error) (servicehost.PreviewDescriptor, Record, uintptr, error) {
		return servicehost.PreviewDescriptor{}, Record{}, 0, err
	}
	if p != nil && p.poisoned.Load() {
		return reject(ErrRootFatal)
	}
	if p == nil || !p.accepted.Load() || p.loaded == nil || p.scope == nil || !rootLive(ctx) {
		return reject(ErrRootDenied)
	}
	token, err := p.loaded.QueryToken()
	if err != nil {
		return reject(p.reject(err))
	}
	if err = p.scope.Recheck(ctx); err != nil {
		return reject(p.reject(err))
	}
	data, err := p.loaded.Record()
	if err != nil {
		return reject(p.reject(err))
	}
	defer clear(data)
	subject, err := p.loaded.Subject()
	if err != nil {
		return reject(p.reject(err))
	}
	preview, r, err := resolvePreviewRecord(data, subject, time.Now())
	if err != nil {
		return reject(p.reject(err))
	}
	if !rootLive(ctx) || !p.accepted.Load() {
		return reject(ErrRootDenied)
	}
	return preview, r, token, nil
}

// privateな純粋resolver。呼出元previewRecordだけが保持rootの照合を担当する。
// digestは照合済みexact record bytesを対象とし、独自再marshalを追加しない。
func resolvePreviewRecord(data []byte, subject Subject, at time.Time) (servicehost.PreviewDescriptor, Record, error) {
	reject := func() (servicehost.PreviewDescriptor, Record, error) {
		return servicehost.PreviewDescriptor{}, Record{}, ErrRecord
	}
	r, err := DecodeRecord(data)
	if err != nil || at.IsZero() || subject.ServiceName != r.Profile.ServiceName || subject.ServiceSID != r.Profile.Keys.ServiceSID || subject.ProcessUserSID != r.Profile.Keys.ProcessUserSID {
		return reject()
	}
	policy, err := backend.PolicyDigest(r.Backend.OperationMode)
	if err != nil {
		return reject()
	}
	rawPolicy, err := hex.DecodeString(policy)
	if err != nil || len(rawPolicy) != 32 {
		return reject()
	}
	keys := make([]ssh.PublicKey, 0, len(r.Backend.AllowedKeys))
	fingerprints := make([]string, 0, len(r.Backend.AllowedKeys))
	for _, value := range r.Backend.AllowedKeys {
		raw, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil {
			return reject()
		}
		key, err := ssh.ParsePublicKey(raw)
		if err != nil || key.Type() != ssh.KeyAlgoED25519 {
			return reject()
		}
		keys = append(keys, key)
		fingerprints = append(fingerprints, ssh.FingerprintSHA256(key))
	}
	keyDigest, err := runbackend.KeySetDigest(keys)
	if err != nil {
		return reject()
	}
	slices.Sort(fingerprints)
	fingerprints = slices.Compact(fingerprints)
	preview := servicehost.PreviewDescriptor{ConfigDigest: sha256.Sum256(data), KeySetDigest: keyDigest, AllowedKeyFingerprints: fingerprints, PolicyMode: r.Backend.OperationMode, HardDeadline: at.Add(r.Backend.MaximumDuration), LeaseCeiling: r.Backend.LeaseDuration}
	copy(preview.PolicyDigest[:], rawPolicy)
	return preview, r, nil
}
