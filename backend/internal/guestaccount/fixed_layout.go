package guestaccount

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
)

// FixedLayout は非秘密の固定配置値のdecode結果であり、配置元の認証や実行許可ではない。
// fieldと可変sliceを公開しない。token/handle/session/承認を補完しない。
type FixedLayout struct {
	profile                 stagingProfile
	machine, user, operator string
	policy                  limitedPolicy
	digest                  [32]byte
	configured              bool
}

// DecodeFixedLayout は固定配置JSONを検証して所有する。認証・digest照合・native取得は行わない。
func DecodeFixedLayout(data []byte) (*FixedLayout, error) {
	if !strictFixedLayout(data) {
		return nil, ErrUnconfigured
	}
	var w fixedLayoutWire
	if json.Unmarshal(data, &w) != nil {
		return nil, ErrUnconfigured
	}
	f, e := fixedLayoutConvert(w)
	if e != nil {
		return nil, e
	}
	if !validFixedLayout(w, f) {
		return nil, ErrUnconfigured
	}
	f.digest = sha256.Sum256(data)
	f.configured = true
	return f, nil
}

// MatchesService は記録内の4値との完全一致だけを返す。実主体を照会しない。
func (f *FixedLayout) MatchesService(name, userSID, serviceSID, operatorSID string) bool {
	return f != nil && f.configured && name == f.profile.serviceName && userSID == f.user && serviceSID == f.profile.serviceSID && operatorSID == f.operator
}

// IncludesConfigurationはconfigurationのpathと同じvolume/file IDを照合する。
func (f *FixedLayout) IncludesConfiguration(path string, volume uint64, id [16]byte) bool {
	if f == nil || !f.configured {
		return false
	}
	v, ok := f.profile.file(path)
	return ok && v.role == roleConfiguration && v.identity.volume == volume && v.identity.fileID == id
}

// IncludesArtifact は固定artifact roleへの包含だけを返す。実hash検査の代わりにならない。
func (f *FixedLayout) IncludesArtifact(path string) bool { return f.includes(path, roleArtifact) }

// IncludesHostKey は固定hostKey roleへの包含だけを返す。
func (f *FixedLayout) IncludesHostKey(path string) bool { return f.includes(path, roleHostKey) }

// Digest はdecodeに渡されたexact byte列のSHA-256を値で返す。
func (f *FixedLayout) Digest() [32]byte {
	if f == nil || !f.configured {
		return [32]byte{}
	}
	return f.digest
}
func (f *FixedLayout) includes(path string, role permissionRole) bool {
	if f == nil || !f.configured {
		return false
	}
	for _, v := range f.profile.files {
		if strings.EqualFold(path, v.path) && v.role == role {
			return true
		}
	}
	return false
}
