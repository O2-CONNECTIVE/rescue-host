package bootstrapauth

import (
	"context"
	"errors"
	"reflect"
	"sync"
)

// Peer は固定 service の発信元だけを証明します。grant の認可は上位の責任です。
// read handle は借用し、この package では payload を読みも close もしません。
type Peer struct{ s *scope }

// ServiceScope は既存 primary token と現在の固定 service の照合結果です。
type ServiceScope struct{ s *scope }
type scope struct {
	mu                                      sync.Mutex
	p                                       Profile
	api                                     kernel
	thread                                  uint32
	pipe, token                             uintptr
	service                                 serviceHandle
	process                                 processHandle
	generation                              processState
	images                                  []*imageEvidence
	refs                                    []owned
	serviceMode, accepted, closed, poisoned bool
}

// Authenticate は caller が固定した専用 OS thread で payload より先に呼びます。
// 途中で失敗しても所有物がある場合は Peer と error を同時に返します。
func (f *Factory) Authenticate(ctx context.Context, borrowedServerRead uintptr) (*Peer, error) {
	if f == nil || !f.configured {
		return nil, ErrConfig
	}
	if !realHandle(borrowedServerRead) || !live(ctx) {
		return nil, ErrDenied
	}
	s, e := f.begin(ctx)
	if e != nil {
		return nil, e
	}
	s.pipe = borrowedServerRead
	peer := &Peer{s: s}
	e = s.start(ctx)
	if final := s.subject(); final != nil {
		e = final
	}
	if e != nil {
		s.accepted = false
		if e == ErrFatal {
			s.poisoned = true
		}
	}
	return peer, e
}

// VerifyService は新しい token を作らず、caller の既存 primary token を借用します。
func (f *Factory) VerifyService(ctx context.Context, borrowedExistingPrimaryToken uintptr) (*ServiceScope, error) {
	if f == nil || !f.configured {
		return nil, ErrConfig
	}
	if !realHandle(borrowedExistingPrimaryToken) || !live(ctx) {
		return nil, ErrDenied
	}
	s, e := f.begin(ctx)
	if e != nil {
		return nil, e
	}
	s.token = borrowedExistingPrimaryToken
	s.serviceMode = true
	proof := &ServiceScope{s: s}
	e = s.start(ctx)
	if final := s.subject(); final != nil {
		e = final
	}
	if e != nil {
		s.accepted = false
		if e == ErrFatal {
			s.poisoned = true
		}
	}
	return proof, e
}
func (f *Factory) begin(ctx context.Context) (*scope, error) {
	api := f.api
	if api == nil {
		api = platformKernel()
	}
	if api == nil {
		return nil, ErrClosed
	}
	s := &scope{p: f.profile, api: api, thread: api.threadID()}
	if e := s.subject(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *scope) subject() error {
	if s.poisoned || s.thread == 0 || s.api.threadID() != s.thread || s.api.cleanThread() != nil {
		s.poisoned = true
		return ErrFatal
	}
	ident, e := s.api.principal(0)
	if e != nil || !s.validIdentity(ident) || s.api.threadID() != s.thread {
		s.poisoned = true
		return ErrFatal
	}
	return nil
}
func (s *scope) validIdentity(i identity) bool {
	if i.user != s.p.Keys.ProcessUserSID || i.kind != 1 {
		return false
	}
	for _, g := range i.groups {
		if g.sid == s.p.Keys.ServiceSID && g.attributes&4 != 0 && g.attributes&0x10 == 0 {
			return true
		}
	}
	return false
}
func (s *scope) start(ctx context.Context) error {
	current := s.p.ChildImage
	if s.serviceMode {
		current = s.p.SupervisorImage
	}
	if e := s.openImage(ctx, current); e != nil {
		return e
	}
	path, e := s.api.currentImage()
	if e != nil || path != current.Path {
		return denied(e)
	}
	if !live(ctx) {
		return ErrDenied
	}
	var pid uint32
	if s.serviceMode {
		pid = s.api.currentPID()
		if e := s.borrowedIdentity(); e != nil {
			return e
		}
	} else {
		pipe, e := s.api.inspectPipe(s.pipe)
		if e != nil || !s.validPipe(pipe) {
			return denied(e)
		}
		pid = pipe.pid
	}
	if pid == 0 {
		return ErrDenied
	}
	svc, e := s.api.openService(s.p.ServiceName)
	if svc != nil {
		s.service = svc
		s.refs = append(s.refs, svc)
	}
	if e != nil || svc == nil {
		return denied(e)
	}
	status, e := svc.status()
	if e != nil || status.state != 4 || status.pid != pid {
		return denied(e)
	}
	if !s.serviceMode {
		process, e := s.api.openProcess(pid)
		if process != nil {
			s.process = process
			s.refs = append(s.refs, process)
		}
		if e != nil || process == nil {
			return denied(e)
		}
		state, e := process.inspect()
		if e != nil || !s.validProcess(state, pid) {
			return denied(e)
		}
		s.generation = state
		if e := s.openImage(ctx, s.p.SupervisorImage); e != nil {
			return e
		}
	}
	if e := s.verify(ctx); e != nil {
		return e
	}
	s.accepted = true
	return nil
}
func (s *scope) borrowedIdentity() error {
	actual, e := s.api.principal(s.token)
	if e != nil || !s.validIdentity(actual) {
		return denied(e)
	}
	current, e := s.api.principal(0)
	if e != nil {
		return ErrFatal
	}
	if !reflect.DeepEqual(actual, current) {
		return ErrDenied
	}
	return nil
}
func (s *scope) validPipe(p pipeState) bool {
	// FILE_TYPE_PIPE、server/message、message-read/wait。書込/新 instance 等の余分な権限も拒否。
	if p.kind != 3 || p.flags != 5 || p.mode != 2 || p.access&0x120089 != 0x120089 || p.access & ^uint32(0x120089) != 0 || p.owner != s.p.Keys.ProcessUserSID || !p.protected || len(p.acl) != 1 || p.pid == 0 {
		return false
	}
	a := p.acl[0]
	return a.Type == 0 && a.Flags == 0 && a.SID == s.p.Keys.ServiceSID && (a.Mask == 0x10000000 || a.Mask == 0x1f01ff)
}
func (s *scope) validProcess(p processState, pid uint32) bool {
	return p.pid == pid && p.creation != 0 && p.alive && p.path == s.p.SupervisorImage.Path && s.validIdentity(p.identity)
}
func (s *scope) verify(ctx context.Context) error {
	if e := s.subject(); e != nil {
		return e
	}
	if !live(ctx) {
		return ErrDenied
	}
	if e := s.origin(); e != nil {
		return e
	}
	for _, image := range s.images {
		if e := image.verify(ctx, s); e != nil {
			return e
		}
	}
	if e := s.origin(); e != nil {
		return e
	}
	if e := s.subject(); e != nil {
		return e
	}
	if !live(ctx) {
		return ErrDenied
	}
	return nil
}
func (s *scope) origin() error {
	current := s.p.ChildImage
	if s.serviceMode {
		current = s.p.SupervisorImage
	}
	path, e := s.api.currentImage()
	if e != nil || path != current.Path {
		return denied(e)
	}
	pid := s.api.currentPID()
	if s.serviceMode {
		if e := s.borrowedIdentity(); e != nil {
			return e
		}
	} else {
		pipe, e := s.api.inspectPipe(s.pipe)
		if e != nil || !s.validPipe(pipe) || pipe.pid != s.generation.pid {
			return denied(e)
		}
		pid = pipe.pid
		state, e := s.process.inspect()
		if e != nil || !s.validProcess(state, pid) || !reflect.DeepEqual(state, s.generation) {
			return denied(e)
		}
	}
	status, e := s.service.status()
	if e != nil || status.state != 4 || status.pid != pid || pid == 0 {
		return denied(e)
	}
	return nil
}
func (p *Peer) Recheck(ctx context.Context) error {
	if p == nil {
		return ErrClosed
	}
	return recheck(ctx, p.s)
}
func (p *ServiceScope) Recheck(ctx context.Context) error {
	if p == nil {
		return ErrClosed
	}
	return recheck(ctx, p.s)
}
func recheck(ctx context.Context, s *scope) error {
	if s == nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrFatal
	}
	if s.closed {
		return ErrClosed
	}
	if !s.accepted {
		return ErrDenied
	}
	e := s.verify(ctx)
	if final := s.subject(); final != nil {
		e = final
	}
	if e != nil {
		s.accepted = false
		if e == ErrFatal {
			s.poisoned = true
		}
	}
	return e
}
func (p *Peer) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return closeScope(ctx, p.s)
}
func (p *ServiceScope) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return closeScope(ctx, p.s)
}

// Close は認証を失効させ、既所有 handle の回収だけを行います。
// foreign/dirty/poisoned の場合も新たな認証 query/read/open へ戻りません。
func closeScope(ctx context.Context, s *scope) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = false
	if s.closed {
		if s.poisoned {
			return ErrFatal
		}
		return nil
	}
	if !s.poisoned {
		if s.thread == 0 || s.api.threadID() != s.thread {
			s.poisoned = true
		} else {
			_ = s.subject()
		}
	}
	unconfirmed := false
	if !s.closed {
		for i := len(s.refs) - 1; i >= 0; i-- {
			if s.refs[i] == nil {
				continue
			}
			if !live(ctx) {
				unconfirmed = true
				break
			}
			err := s.refs[i].close()
			if err == nil {
				s.refs[i] = nil
				continue
			}
			unconfirmed = true
			if errors.Is(err, ErrFatal) {
				s.poisoned = true
			}
		}
		for _, ref := range s.refs {
			if ref != nil {
				unconfirmed = true
			}
		}
		s.closed = !unconfirmed
	}
	if s.poisoned {
		if unconfirmed {
			return errors.Join(ErrFatal, ErrUnconfirmed)
		}
		return ErrFatal
	}
	if unconfirmed {
		return ErrUnconfirmed
	}
	return nil
}

func live(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
func realHandle(h uintptr) bool { return h != 0 && h < ^uintptr(0)-15 }

// native 境界が検出した thread の不明を通常の発信元拒否へ縮小しません。
func denied(err error) error {
	if err == ErrFatal {
		return ErrFatal
	}
	return ErrDenied
}
