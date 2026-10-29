package guestaccount

import (
	"context"
	"sync"
)

// accountDiagnosticは読取診断だけであり、作成元・不在・完全権限の証拠でない。
type accountDiagnostic struct {
	binding   binding
	name, sid string
	flags     uint32
}

// accountQueryScopeFatalを返したcallはOS threadをunlockしていない。
// callerは他のnative操作やhealthy worker poolへの復帰を行わず、当該
// goroutineを終了してthreadを退役させる。q/kernelの未知資源参照は保持する。
// このmarkerを外側compositionで認識/伝播するService配線は未接続である。
type accountQueryScopeFatal struct{}

func (accountQueryScopeFatal) Error() string {
	return "一時Guest照会のscopeが未確定です。呼出しworkerの退役が必要です"
}
func (accountQueryScopeFatal) Unwrap() error { return ErrUnknown }

type accountQueryPrincipal struct {
	user                                  string
	kind                                  uint32
	threadToken, restricted, appContainer bool
	tokenID, authenticationID, modifiedID uint64
}
type accountQueryObservation struct {
	kind        accountQueryKind
	name, sid   string
	flags, role uint32
}
type accountQueryKind uint8

const (
	accountQueryRole accountQueryKind = iota + 1
	accountQueryDomain
	accountQueryUser
)

type accountQueryMemory interface {
	decode() (accountQueryObservation, error)
	// 複数native資源を含む場合も、個々のFree/Close直前に同scopeを確認する。
	release(scopeGuard func() bool) error
}

// 境界は読取APIとその返却資源のみ。mutationや資格情報を持ち込まない。
type accountQueryKernel interface {
	threadID() uint32
	principal() (accountQueryPrincipal, error)
	localRole() (accountQueryMemory, error)
	localDomain() (accountQueryMemory, error)
	user23(string) (accountQueryMemory, error)
}
type accountQuery struct {
	self    *accountQuery
	mu      sync.Mutex
	plan    Plan
	binding binding
	seal    *seal
	kernel  accountQueryKernel
	pending []accountQueryMemory
	fatal   bool
}

func newAccountQuery(p Plan, b binding, s *seal, k accountQueryKernel) (*accountQuery, error) {
	if !p.validBinding(b) || s == nil || k == nil {
		return nil, ErrUnconfigured
	}
	q := &accountQuery{plan: p, binding: b, seal: s, kernel: k}
	q.self = q
	return q, nil
}
func (q *accountQuery) valid() bool { return q != nil && q.self == q && q.kernel != nil }

// inspectは完全な権限producer未設定を必ず返す。診断をcoreの完了へ昇格しない。
func (q *accountQuery) inspect(ctx context.Context, b binding) (accountSnapshot, error) {
	d, err := q.query(ctx, b)
	if err != nil {
		return accountSnapshot{}, err
	}
	return accountSnapshot{seal: q.seal, binding: d.binding, presence: exact, disabled: d.flags&2 != 0, complete: false}, ErrUnconfigured
}
