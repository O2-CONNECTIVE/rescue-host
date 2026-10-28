// Package localcontrol は製品gate閉鎖中のWindows制御IPCの認可境界である。
// 実pipeの受付・service導入・OS保護の成立は提供しない。
package localcontrol

import (
	"errors"
	"strconv"
	"strings"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
)

var (
	ErrConfig      = errors.New("制御IPCの設定が不正です")
	ErrDenied      = errors.New("制御要求を許可しません")
	ErrFatal       = errors.New("実行主体の安全な復元を確認できません")
	ErrClosed      = errors.New("制御IPCの起動ゲートは閉鎖されています")
	ErrUnconfirmed = errors.New("制御処理の完了を確認できません")
)

type Config struct {
	OperatorSID, ServiceSID, GuestSID string
	Generation                        uint64
	Identity                          backend.Identity
	ChannelNonce                      string
}

type PipePlan struct {
	Name, SDDL                                                  string
	OpenMode, PipeMode, ClientAccess, MaxInstances, BufferBytes uint32
}

type Policy struct {
	config Config
	plan   PipePlan
}

func NewPolicy(config Config) (*Policy, error) {
	if !userSID(config.OperatorSID) || !userSID(config.GuestSID) || !(userSID(config.ServiceSID) || serviceSID(config.ServiceSID)) || config.OperatorSID == config.ServiceSID || config.OperatorSID == config.GuestSID || config.ServiceSID == config.GuestSID || config.Generation == 0 || !config.Identity.Valid() || !token(config.Identity.LaunchID) || !decimal(config.Identity.Epoch) || !hex(config.ChannelNonce) {
		return nil, ErrConfig
	}
	plan := PipePlan{Name: `\\.\pipe\rescue-host-control-` + strconv.FormatUint(config.Generation, 10) + "-" + config.ChannelNonce, OpenMode: 0x40080003, PipeMode: 0x0000000e, ClientAccess: 0x00100183, MaxInstances: 1, BufferBytes: 16384}
	plan.SDDL = "D:P(D;;0x001f01ff;;;" + config.GuestSID + ")(A;;0x001f01ff;;;" + config.ServiceSID + ")(A;;0x00100183;;;" + config.OperatorSID + ")"
	return &Policy{config: config, plan: plan}, nil
}

func sid(value, prefix string, count int) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), "-")
	if len(parts) != count {
		return false
	}
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}
func userSID(value string) bool    { return sid(value, "S-1-5-21-", 4) }
func serviceSID(value string) bool { return sid(value, "S-1-5-80-", 5) }
func decimal(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && strconv.FormatUint(n, 10) == value
}
func token(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func (p *Policy) Plan() PipePlan { return p.plan }
