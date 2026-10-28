package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// IssuedLaunch はHostの現在issuerと同じAuthorityから取得する非秘密相関値です。
// fieldは外部から構築せず、識別子そのものを承認やOS認証と扱いません。
type IssuedLaunch struct {
	instance [32]byte
	binding  supervisor.Binding
}

func (i IssuedLaunch) InstanceEpoch() [32]byte     { return i.instance }
func (i IssuedLaunch) Binding() supervisor.Binding { return i.binding }

type IssuedLauncher interface {
	StartIssued(context.Context, *supervisor.Authority, IssuedLaunch) (Process, error)
}
