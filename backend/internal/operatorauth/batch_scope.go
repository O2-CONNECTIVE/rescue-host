package operatorauth

import (
	"context"
	"errors"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

// batchServiceScope は固定 worker が取得した証拠の private memory seam。
// 保護 Factory 以外へ新しい公開 constructor や callback は設けない。
type batchServiceScope interface {
	Recheck(context.Context) error
	Close(context.Context) error
}

type batchScopeOps struct {
	verify   func(context.Context, uintptr) (batchServiceScope, error)
	threadID func() uint32
	clean    func() error
}

type batchScope struct {
	mu                           sync.Mutex
	token                        uintptr
	ops                          batchScopeOps
	proof                        batchServiceScope
	thread                       uint32
	attempted, uncertain, closed bool
	fatal                        error
}

func (s *batchScope) acquire(ctx context.Context) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempted || s.closed || s.ops.verify == nil || s.ops.threadID == nil || s.ops.clean == nil {
		return ErrUnconfigured
	}
	s.attempted = true
	s.thread = s.ops.threadID()
	if s.thread == 0 {
		s.fatal = ErrFatal
		return s.fatal
	}
	// constructor が返る前の panic は、未知の所有参照を成功へ縮小しない。
	s.uncertain = true
	s.proof, err = s.ops.verify(ctx, s.token)
	s.uncertain = false
	if errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, ErrFatal) {
		s.fatal = errors.Join(ErrFatal, err)
	}
	if s.ops.threadID() != s.thread {
		s.fatal = ErrFatal
	}
	if s.fatal != nil {
		return s.fatal
	}
	if err != nil {
		return err
	}
	if s.proof == nil {
		return ErrDenied
	}
	return nil
}

func (s *batchScope) guard() error {
	if s.uncertain {
		return ErrUnconfirmed
	}
	if s.fatal != nil {
		return s.fatal
	}
	// 未取得・回収済み scope は thread/token query を開始しない。
	if s.proof == nil || s.closed {
		return nil
	}
	if s.ops.threadID() != s.thread {
		s.fatal = ErrFatal
		return s.fatal
	}
	if err := s.ops.clean(); err != nil {
		s.fatal = errors.Join(ErrFatal, err)
		return s.fatal
	}
	return nil
}

func (s *batchScope) clean() error {
	if !s.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer s.mu.Unlock()
	return s.guard()
}

func (s *batchScope) recheck(ctx context.Context) error {
	if !s.mu.TryLock() {
		return ErrDenied
	}
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if s.proof == nil || s.closed {
		return ErrDenied
	}
	err := s.proof.Recheck(ctx)
	if errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, ErrFatal) {
		s.fatal = errors.Join(ErrFatal, err)
		return s.fatal
	}
	return err
}

func (s *batchScope) close(ctx context.Context) error {
	if !s.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}
	if s.proof == nil || s.closed {
		return nil
	}
	err := s.proof.Close(ctx)
	// bootstrap の exact ErrFatal は所有参照を回収済み、join された
	// ErrUnconfirmed は未回収を意味する。どちらも worker の再利用は禁止。
	if err == bootstrapauth.ErrFatal {
		s.closed = true
	}
	if errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, ErrFatal) {
		s.fatal = errors.Join(ErrFatal, err)
		return s.fatal
	}
	if err == nil {
		s.closed = true
	}
	return err
}
