//go:build !windows

package operatorauth

import "context"

// 全OSで公開入口は未設定拒否。非Windowsではnative認証/回収を成功させない。
type nativeThread struct{}

func (*nativeThread) lock()                           {}
func (*nativeThread) unlock()                         {}
func (*nativeThread) clean() error                    { return ErrUnconfigured }
func (*nativeThread) impersonate(uintptr) error       { return ErrUnconfigured }
func (*nativeThread) token() (tokenProjection, error) { return tokenProjection{}, ErrUnconfigured }
func (*nativeThread) revert() error                   { return ErrUnconfigured }
func (*nativeThread) abort()                          { panic("operatorauth: native helper unavailable") }

type nativeProcess struct {
	handle uintptr
}

func (*nativeProcess) terminate() error                     { return ErrUnconfigured }
func (*nativeProcess) wait(context.Context) (uint32, error) { return 0, ErrUnconfigured }
func (*nativeProcess) close() error                         { return ErrUnconfigured }
