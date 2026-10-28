package protectedkey

import (
	"errors"
	"time"
)

var (
	ErrConfig = errors.New("保護されたホスト鍵の構成が不足しています")
	ErrClosed = errors.New("保護されたホスト鍵の実行コンテキストが未実装です")
	ErrFatal  = errors.New("保護されたホスト鍵の実行主体を確認できません")
)

// Profile は保護された配置元の固定 catalog で、要求データではありません。
// 記録は配置証跡の参照であり、OS 保護の成立自体を証明しません。
type Profile struct {
	Version                                           uint32
	ProcessUserSID, ServiceSID, OperatorSID, GuestSID string
	Volume                                            VolumeProfile
	Provisioning                                      ProvisioningRecord
	Catalog                                           map[string]KeyProfile
}

type VolumeProfile struct {
	Root, Filesystem string
	Serial           uint64
}

type ProvisioningRecord struct {
	Reference   string
	ProtectedAt time.Time
}

type NodePolicy struct {
	Path, OwnerSID string
	ACL            []ACE
}

// ACE は不明な条件を拒否するため、native の型・flags・mask・主体を保持します。
type ACE struct {
	Type, Flags uint8
	Mask        uint32
	SID         string
}

// NewNative はファイルを開かず、native 実行 context も有効にしません。
func NewNative(profile Profile) (*Loader, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	profile = copyProfile(profile)
	return &Loader{catalog: profile.Catalog, source: nativeSource{profile: profile}}, nil
}

func copyProfile(p Profile) Profile {
	catalog := make(map[string]KeyProfile, len(p.Catalog))
	for ref, key := range p.Catalog {
		key.Nodes = append([]NodePolicy(nil), key.Nodes...)
		for i := range key.Nodes {
			key.Nodes[i].ACL = append(make([]ACE, 0, len(key.Nodes[i].ACL)), key.Nodes[i].ACL...)
		}
		catalog[ref] = key
	}
	p.Catalog = catalog
	return p
}
