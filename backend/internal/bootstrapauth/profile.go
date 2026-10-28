package bootstrapauth

import (
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

var (
	ErrConfig      = errors.New("bootstrap 認証の保護された構成が不足しています")
	ErrDenied      = errors.New("bootstrap の発信元を確認できません")
	ErrFatal       = errors.New("bootstrap 認証 thread の主体が不明です")
	ErrClosed      = errors.New("bootstrap 認証 scope が閉鎖されています")
	ErrUnconfirmed = errors.New("bootstrap 認証 handle の回収を確認できません")
)

// Profile は保護された配置元の入力です。UI・env・argv から構築しても認証にはなりません。
type Profile struct {
	Keys                        protectedkey.Profile
	ServiceName                 string
	SupervisorImage, ChildImage Image
}

// Image の配置記録と ACL は過去の保護成立を証明しません。
type Image struct {
	Path     string
	SHA256   [32]byte
	MaxBytes int64
	Volume   protectedkey.VolumeProfile
	Nodes    []protectedkey.NodePolicy
}

// Factory の構築自体は caller 認証を行いません。
type Factory struct {
	profile    Profile
	api        kernel
	configured bool
}

// NewNativeFactory は静的検証と複製だけを行います。
func NewNativeFactory(p Profile) (*Factory, error) {
	if _, err := protectedkey.NewNative(p.Keys); err != nil {
		return nil, ErrConfig
	}
	if !serviceName(p.ServiceName) || !validImage(p, p.ChildImage) || !validImage(p, p.SupervisorImage) {
		return nil, ErrConfig
	}
	return &Factory{profile: copyProfile(p), configured: true}, nil
}
