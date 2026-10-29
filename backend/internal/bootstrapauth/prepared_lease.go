package bootstrapauth

import (
	"context"
	"reflect"
	"sync"
)

// PreparedLeaseは同じPreparedServiceのgateを保持する限定resource lease。
// SCM、実tokenの発行、承認やguestの権限を証明しない。
type PreparedLease struct {
	self     *PreparedLease
	mu       sync.Mutex
	prepared *PreparedService
	s        *scope
	gate     chan struct{}
	pid      uint32
	expected Profile
	closed   bool
}

// HoldProfileは元callerの固定threadだけで呼ぶ。静的検証・copyは認証ではない。
// admission後は失敗時も部分leaseを返す。実returnまでgateを解放しない。
func (p *PreparedService) HoldProfile(ctx context.Context, expected Profile) (*PreparedLease, error) {
	if p == nil || p.s == nil || p.gate == nil {
		return nil, ErrClosed
	}
	f, e := NewNativeFactory(expected)
	if e != nil {
		return nil, e
	}
	if !p.enter(ctx) {
		return nil, ErrDenied
	}
	l := &PreparedLease{prepared: p, s: p.s, gate: p.gate, pid: p.pid, expected: f.profile}
	l.self = l
	return l, l.verify(ctx)
}
func (l *PreparedLease) valid() bool {
	return l != nil && l.self == l && l.prepared != nil && l.s != nil && l.gate != nil
}
func (l *PreparedLease) same() bool {
	return l.prepared.s == l.s && l.prepared.gate == l.gate && l.prepared.pid == l.pid
}
func (l *PreparedLease) poison() { l.s.accepted = false; l.s.poisoned = true }
func (l *PreparedLease) verify(ctx context.Context) (result error) {
	defer func() {
		if recover() != nil {
			l.poison()
			result = ErrFatal
		}
	}()
	if !l.same() {
		l.poison()
		return ErrFatal
	}
	if l.s.poisoned {
		return ErrFatal
	}
	if l.s.closed {
		return ErrClosed
	}
	if !l.s.accepted {
		return ErrDenied
	}
	if !reflect.DeepEqual(l.expected, l.s.p) {
		return l.prepared.finish(ErrConfig)
	}
	return l.prepared.finish(l.prepared.verify(ctx))
}

// Recheckは新workerを起動せず、元scopeの自主体・保持imageを実照合する。
// 並行処理の取消は、その実returnやleaseの解放確認の代わりにならない。
func (l *PreparedLease) Recheck(ctx context.Context) error {
	if !l.valid() {
		return ErrClosed
	}
	if !l.mu.TryLock() {
		return ErrDenied
	}
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if l.s.poisoned {
		return ErrFatal
	}
	if !live(ctx) {
		return ErrDenied
	}
	return l.verify(ctx)
}

// Closeはhealthyな同scopeで論理gateだけを返し、PreparedServiceやimageを
// closeしない。fatal/panicでは元refsとgateを保持し、poolへ戻さない。
func (l *PreparedLease) Close(ctx context.Context) (result error) {
	if !l.valid() {
		return ErrClosed
	}
	if !l.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer l.mu.Unlock()
	defer func() {
		if recover() != nil {
			l.poison()
			result = ErrFatal
		}
	}()
	if l.closed {
		return nil
	}
	if l.s.poisoned {
		return ErrFatal
	}
	if !live(ctx) {
		return ErrUnconfirmed
	}
	if !l.same() {
		l.poison()
		return ErrFatal
	}
	if e := l.s.subject(); e != nil {
		l.poison()
		return e
	}
	if !live(ctx) {
		return ErrUnconfirmed
	}
	select {
	case <-l.gate:
		l.closed = true
		return nil
	default:
		l.poison()
		return ErrFatal
	}
}
