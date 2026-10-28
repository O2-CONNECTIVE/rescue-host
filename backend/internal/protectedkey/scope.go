package protectedkey

import (
	"context"
	"reflect"
	"sync"
)

type scopeKey struct{}
type nativeKernel interface {
	kernel
	threadID() uint32
}

// NativeScope は認可済み receiver が既に thread を固定した専用 goroutine で所有します。
// OS の主体を検査しますが、receiver の認可は上位層の責任です。
// ErrFatal の場合は UnlockOSThread せず goroutine を終了し、別 worker で再試行しません。
// この library は Goexit、RevertToSelf、権限変更 API を呼びません。
type NativeScope struct {
	mu               sync.Mutex
	ctx              context.Context
	profile          Profile
	api              nativeKernel
	thread           uint32
	closed, poisoned bool
}

// BeginNativeScope は有効な期限と、呼出側による LockOSThread を要求します。
// 呼出側の thread 固定や receiver としての認可を証明するものではありません。
func BeginNativeScope(ctx context.Context, profile Profile) (*NativeScope, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	if !liveDeadline(ctx) {
		return nil, ErrDenied
	}
	return platformScope(ctx, copyProfile(profile))
}

func newNativeScope(ctx context.Context, p Profile, api nativeKernel) (*NativeScope, error) {
	if !liveDeadline(ctx) || api == nil {
		return nil, ErrDenied
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	thread := api.threadID()
	if thread == 0 || (&securedSource{profile: p, api: api}).checkPrincipal(ctx) != nil || api.threadID() != thread {
		return nil, ErrFatal
	}
	return &NativeScope{ctx: ctx, profile: copyProfile(p), api: api, thread: thread}, nil
}

// Context は非公開の scope 値を運び、通常の context では読取りを拒否します。
func (s *NativeScope) Context() context.Context {
	if s == nil {
		return context.Background()
	}
	return context.WithValue(s.ctx, scopeKey{}, s)
}

func (s *NativeScope) Close() error {
	if s == nil {
		return ErrFatal
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrFatal
	}
	if s.closed {
		return nil
	}
	if s.api.threadID() != s.thread || (&securedSource{profile: s.profile, api: s.api}).checkPrincipal(context.Background()) != nil || s.api.threadID() != s.thread {
		s.poisoned = true
		return ErrFatal
	}
	s.closed = true
	return nil
}

type nativeSource struct{ profile Profile }

func (n nativeSource) read(ctx context.Context, key KeyProfile) ([]byte, error) {
	s, ok := ctx.Value(scopeKey{}).(*NativeScope)
	if !ok || s == nil {
		return nil, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline, valid := ctx.Deadline()
	original, originalValid := s.ctx.Deadline()
	if s.poisoned || s.closed || !valid || !originalValid || deadline != original || !reflect.DeepEqual(s.profile, n.profile) || s.api.threadID() != s.thread {
		s.poisoned = true
		return nil, ErrFatal
	}
	data, err := (&securedSource{profile: s.profile, api: s.api}).read(ctx, key)
	if err == ErrFatal || s.api.threadID() != s.thread {
		clear(data)
		s.poisoned = true
		return nil, ErrFatal
	}
	return data, err
}

func liveDeadline(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
