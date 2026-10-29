package guestaccount

import (
	"context"
	"runtime"
)

func (q *accountQuery) query(ctx context.Context, b binding) (diagnostic accountDiagnostic, result error) {
	if !q.valid() {
		return diagnostic, ErrUnconfigured
	}
	if !finite(ctx) || b != q.binding || !q.plan.validBinding(b) {
		return diagnostic, ErrDenied
	}
	if !q.mu.TryLock() {
		return diagnostic, ErrBusy
	}
	defer q.mu.Unlock()
	if q.fatal {
		return diagnostic, ErrUnknown
	}
	if len(q.pending) != 0 {
		return diagnostic, ErrCleanup
	}
	if !finite(ctx) {
		return diagnostic, ErrDenied
	}
	runtime.LockOSThread()
	defer func() {
		if q.fatal {
			diagnostic = accountDiagnostic{}
			result = accountQueryScopeFatal{}
			return // 不明なthreadをhealthy poolへ返さず、callerに退役を要求する。
		}
		runtime.UnlockOSThread()
	}()
	var scopeGuard func() bool
	defer func() {
		if recover() != nil {
			q.fatal = true
		}
		// ctxの期限とは独立に、健康な同scopeだけで資源を解放する。
		if !q.fatal && !q.finalCheck(scopeGuard) {
			q.fatal = true
		}
		if !q.fatal && !q.releasePending(scopeGuard) {
			diagnostic = accountDiagnostic{}
			result = ErrCleanup
		}
		// 最後のFreeによる主体変化も、ctx取消で短絡せず検査する。
		if !q.fatal && !q.finalCheck(scopeGuard) {
			q.fatal = true
		}
		if q.fatal {
			diagnostic = accountDiagnostic{}
			result = ErrUnknown
			return
		}
		if result == nil && !finite(ctx) {
			diagnostic = accountDiagnostic{}
			result = ErrDenied
		}
	}()
	thread := q.kernel.threadID()
	principal, err := q.kernel.principal()
	if err != nil || thread == 0 || !q.primary(principal) {
		q.fatal = true
		return diagnostic, ErrUnknown
	}
	guard := func() bool {
		if q.fatal {
			return false
		}
		if q.kernel.threadID() != thread {
			q.fatal = true
			return false
		}
		now, e := q.kernel.principal()
		if e != nil || now != principal || !q.primary(now) {
			q.fatal = true
			return false
		}
		return true
	}
	scopeGuard = guard
	role, err := q.read(ctx, accountQueryRole, guard)
	// DSROLE_MACHINE_ROLE 0..3だけを許可。4/5はDC、未知値も拒否。
	if err != nil || role.role > 3 {
		return diagnostic, ErrDenied
	}
	domain, err := q.read(ctx, accountQueryDomain, guard)
	if err != nil || domain.sid != b.machine {
		return diagnostic, ErrDenied
	}
	user, err := q.read(ctx, accountQueryUser, guard)
	if err != nil || user.name != b.name || user.sid != b.sid || !finite(ctx) || !guard() {
		return diagnostic, ErrDenied
	}
	afterRole, err := q.read(ctx, accountQueryRole, guard)
	if err != nil || afterRole.role != role.role {
		return diagnostic, ErrDenied
	}
	afterDomain, err := q.read(ctx, accountQueryDomain, guard)
	if err != nil || afterDomain.sid != domain.sid || !finite(ctx) || !guard() {
		return diagnostic, ErrDenied
	}
	return accountDiagnostic{binding: b, name: user.name, sid: user.sid, flags: user.flags}, nil
}
func (q *accountQuery) finalCheck(guard func() bool) (ok bool) {
	defer func() {
		if recover() != nil {
			q.fatal = true
			ok = false
		}
	}()
	return guard != nil && guard()
}
func (q *accountQuery) primary(p accountQueryPrincipal) bool {
	return p.user == q.binding.service && p.kind == 1 && !p.threadToken && !p.restricted && !p.appContainer && p.tokenID != 0 && p.authenticationID != 0 && p.modifiedID != 0
}
func (q *accountQuery) read(ctx context.Context, kind accountQueryKind, guard func() bool) (accountQueryObservation, error) {
	if !finite(ctx) || !guard() {
		return accountQueryObservation{}, ErrDenied
	}
	var memory accountQueryMemory
	var err error
	switch kind {
	case accountQueryRole:
		memory, err = q.kernel.localRole()
	case accountQueryDomain:
		memory, err = q.kernel.localDomain()
	case accountQueryUser:
		memory, err = q.kernel.user23(q.binding.name)
	default:
		return accountQueryObservation{}, ErrDenied
	}
	// error付きの部分bufferも、cancel検査より先に所有者へ残す。
	if memory != nil {
		q.pending = append(q.pending, memory)
	}
	if err != nil || memory == nil || !finite(ctx) || !guard() {
		return accountQueryObservation{}, ErrDenied
	}
	observation, err := memory.decode()
	if err != nil || observation.kind != kind || !finite(ctx) || !guard() {
		return accountQueryObservation{}, ErrDenied
	}
	return observation, nil
}
func (q *accountQuery) releasePending(scopeGuard func() bool) bool {
	if !q.valid() || q.fatal {
		return false
	}
	pending := q.pending
	var retained []accountQueryMemory
	for i := len(pending) - 1; i >= 0; i-- {
		if q.fatal || !q.finalCheck(scopeGuard) {
			q.fatal = true
			q.pending = append(pending[:i+1], retained...)
			return false
		}
		memory := pending[i]
		ok := func() (ok bool) {
			defer func() {
				if recover() != nil {
					q.fatal = true
					ok = false
				}
			}()
			return memory.release(scopeGuard) == nil
		}()
		if !ok {
			retained = append(retained, memory)
		}
		if q.fatal {
			q.pending = append(pending[:i], retained...)
			return false
		}
	}
	// Free失敗は保持して新照会を拒否する。解放結果不明を盲再試行しない。
	q.pending = retained
	return len(retained) == 0
}
