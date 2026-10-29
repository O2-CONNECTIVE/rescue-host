// Package operatorauth は未配線の単回operator認証と独立した回収ownerを扱う。
// factoryは未設定拒否であり、実SID/ACL/launcher/report transportを提供しない。
package operatorauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrUnconfigured = errors.New("operator認証の保護構成は未設定です")
	ErrDenied       = errors.New("operator認証を拒否しました")
	ErrFatal        = errors.New("認証helperのthread主体または解放が不明です")
	ErrUnconfirmed  = errors.New("認証helperの回収を確認できません")
)

type tokenProjection struct {
	user                            string
	authentication                  uint64
	session, integrity, kind, level uint32
	restricted, appContainer        bool
}

// token値は将来の保護profileから固定する。SIDだけでlevel等を補完しない。
type helperProfile struct{ operator, guest tokenProjection }
type threadKernel interface {
	lock()
	unlock()
	clean() error
	impersonate(uintptr) error
	token() (tokenProjection, error)
	revert() error
	abort()
}
type helperOps struct {
	thread  threadKernel
	pipe    uintptr
	check   func(context.Context) error
	read    func(context.Context) ([]byte, error)
	publish func(context.Context, []byte) error
}

// Helperは専用process内のsole reader用。constructorは保護launcher未配線のためprivate。
type Helper struct {
	mu      sync.Mutex
	used    bool
	profile helperProfile
	ops     helperOps
}

// RunFixedはargvをlocator照合にだけ用い、期待値と権限は固定保護sourceに要求する。
// source未構成の既定状態には解除flagがない。
func RunFixed(ctx context.Context, args ...string) error { return runNativeFixedHelper(ctx, args) }

func finite(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}

// Readは将来のnative factoryだけが作る単回消費権で、wireやSID文字列からは作れない。
type Read struct {
	owner    *Collector
	request  context.Context
	frame    []byte
	deadline time.Time
	used     bool
}

type reportResult struct {
	frame []byte
	eof   bool
}
type collectorOps struct {
	check        func(context.Context) error
	drain        func(context.Context) (reportResult, error)
	terminate    func() error
	wait         func(context.Context) (uint32, error)
	closeIO      func(context.Context) error
	closeProcess func() error
	revoke       func() error
	reclaim      func(context.Context) error
}

// Collectorはhelperと別processの健康なSupervisor側に置く。
// opsは将来の保持launch/同Serviceからだけ構成し、一般callerの注入口を提供しない。
type Collector struct {
	mu                                                              sync.Mutex
	cleaning                                                        sync.Mutex
	used, failed, closed, busy                                      bool
	proof                                                           *Read
	ops                                                             collectorOps
	cleanup                                                         time.Duration
	revoked, terminated, waited, ioClosed, processClosed, reclaimed bool
	revokeErr                                                       error
}
