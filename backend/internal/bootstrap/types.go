// Package bootstrap は非秘密の起動記録を扱う純粋 codec である。
// pipe の由来、実 EOF、認証、単回消費、実行許可を証明しない。
package bootstrap

import (
	"errors"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

const Protocol = "rescue-host-bootstrap/1"
const RecordBytes = 16384

var ErrRecord = errors.New("起動記録を受理できません")
var ErrExpected = errors.New("起動記録の期待束縛が一致しません")
var ErrLimits = errors.New("起動記録の受信予算が不正です")

// Config は公開鍵と上限だけを持つ。Signer、秘密、任意 path を持たない。
type Config struct {
	OperationMode                             string
	AllowedKeys                               []string
	MaximumDuration, LeaseDuration            time.Duration
	MaxChannels, MaxHandshakes, MaxTransports int
}

// Message は構造検証の結果であり、保護 provisioning の証明ではない。
// Grant を Authority や実行可能な設定へ昇格させる判断は提供しない。
type Message struct {
	IssuerInstanceEpoch string
	Grant               supervisor.ProvisionedGrant
	Wire                control.WireContext
	Config              Config
	HostKeyRef          string
	Limits              stdio.Limits
}

// Expected は将来の認証済み factory が payload と独立に供給する全束縛値。
// この型の構築や一致は認証ではない。payload から作る公開 factory は設けない。
type Expected struct {
	IssuerInstanceEpoch string
	Grant               supervisor.ProvisionedGrant
	Wire                control.WireContext
	Config              Config
	HostKeyRef          string
	Limits              stdio.Limits
}

// ReceiverLimits は保護された受信側が必ず明示する上限。既定値はない。
type ReceiverLimits struct {
	MaxFrames         int
	MaxCleanupTimeout time.Duration
}
