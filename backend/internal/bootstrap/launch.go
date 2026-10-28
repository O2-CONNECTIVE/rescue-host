package bootstrap

import (
	"strconv"
	"strings"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
)

// LaunchValues は同じ issuer/Authority 由来の非秘密相関値です。
// 値の一致や argv の解析は、pipe の由来や caller の認証になりません。
type LaunchValues struct {
	IssuerInstanceEpoch string
	Wire                control.WireContext
}

// UntrustedLaunchArgs は解析しただけの未認証入力です。
// 認証済み context や runtime 設定へ変換する factory は未接続です。
type UntrustedLaunchArgs struct {
	handle uintptr
	values LaunchValues
}

func (a UntrustedLaunchArgs) HandleLocator() uintptr    { return a.handle }
func (a UntrustedLaunchArgs) Correlation() LaunchValues { return a.values }

// FormatLaunchArgs は保護側が固定した非秘密値を並べるだけで、発行権を消費しません。
func FormatLaunchArgs(values LaunchValues, handle uintptr, stdio [3]uintptr) ([]string, error) {
	if !validLaunchValues(values) || !distinctHandles(handle, stdio) {
		return nil, ErrRecord
	}
	w := values.Wire
	parts := [...]string{strconv.FormatUint(uint64(handle), 10), values.IssuerInstanceEpoch, w.Generation, w.Identity.LaunchID, w.Identity.Epoch, w.Identity.PolicyDigest, w.Binding.ArtifactSHA256, w.Binding.ChannelNonce, w.PolicyRef}
	args := make([]string, 10)
	args[0] = "--supervised-internal"
	for i, value := range parts {
		args[i+1] = launchPrefixes[i] + value
	}
	if _, err := ParseLaunchArgs(args, stdio); err != nil {
		return nil, err
	}
	return args, nil
}

var launchPrefixes = [...]string{
	"--bootstrap-handle=", "--bootstrap-issuer=", "--bootstrap-generation=",
	"--bootstrap-launch-id=", "--bootstrap-session-epoch=", "--bootstrap-policy-digest=",
	"--bootstrap-artifact-sha256=", "--bootstrap-channel-nonce=", "--bootstrap-policy-ref=",
}

// ParseLaunchArgs は決められた順の相関値だけを解析します。認証や handle 取得は行いません。
func ParseLaunchArgs(args []string, stdio [3]uintptr) (UntrustedLaunchArgs, error) {
	if len(args) != 10 || args[0] != "--supervised-internal" {
		return UntrustedLaunchArgs{}, ErrRecord
	}
	total := 0
	for _, arg := range args {
		if len(arg) > 4096-total-1 {
			return UntrustedLaunchArgs{}, ErrRecord
		}
		total += len(arg) + 1
		for _, b := range []byte(arg) {
			if b < 0x21 || b > 0x7e {
				return UntrustedLaunchArgs{}, ErrRecord
			}
		}
	}
	var values [9]string
	for i, prefix := range launchPrefixes {
		if !strings.HasPrefix(args[i+1], prefix) {
			return UntrustedLaunchArgs{}, ErrRecord
		}
		values[i] = strings.TrimPrefix(args[i+1], prefix)
	}
	handle, ok := decimal(values[0], strconv.IntSize, true)
	if !ok || !distinctHandles(uintptr(handle), stdio) {
		return UntrustedLaunchArgs{}, ErrRecord
	}
	correlation := LaunchValues{IssuerInstanceEpoch: values[1], Wire: control.WireContext{
		Generation: values[2], Identity: control.WireIdentity{LaunchID: values[3], Epoch: values[4], PolicyDigest: values[5]},
		Binding: control.Binding{ArtifactSHA256: values[6], ChannelNonce: values[7]}, PolicyRef: values[8],
	}}
	if !validLaunchValues(correlation) {
		return UntrustedLaunchArgs{}, ErrRecord
	}
	return UntrustedLaunchArgs{handle: uintptr(handle), values: correlation}, nil
}

func distinctHandles(handle uintptr, stdio [3]uintptr) bool {
	if handle == 0 {
		return false
	}
	for i, value := range stdio {
		if value == 0 || value == handle {
			return false
		}
		for j := 0; j < i; j++ {
			if value == stdio[j] {
				return false
			}
		}
	}
	return true
}

func validLaunchValues(v LaunchValues) bool {
	if !isDigest(v.IssuerInstanceEpoch) {
		return false
	}
	_, err := control.NewState(v.Wire)
	return err == nil
}
