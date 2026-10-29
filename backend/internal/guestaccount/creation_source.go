package guestaccount

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type creationPreflight interface {
	check(context.Context) error
	Close(context.Context) error
}
type ownedAccountInspector interface {
	accountInspector
	Close(context.Context) error
}
type creationOwned interface{ Close(context.Context) error }
type creationDependencies struct {
	preflight creationPreflight
	create    func() (sidExecutor, error)
	inspect   func(binding) (ownedAccountInspector, error)
	cleanup   func() (sidExecutor, error)
}

// creationSourceは一つのjournal intentから新規作成を一度だけ行う。
// 既存accountのname lookupを作成元へ変換せず、完全inspectorを発明しない。
type creationSource struct {
	self              *creationSource
	mu, ownerMu       sync.Mutex
	stop              atomic.Bool
	inspectionUnknown atomic.Bool
	plan              Plan
	seal              *seal
	deps              creationDependencies
	used              bool
	binding           binding
	cleanup           *sidSource
	owners            []creationOwned
}

func newCreationSource(p Plan, s *seal, deps creationDependencies) (*creationSource, error) {
	if !p.valid || p.binding.sid != "" || s == nil || deps.preflight == nil || deps.create == nil || deps.inspect == nil || deps.cleanup == nil {
		return nil, ErrUnconfigured
	}
	c := &creationSource{plan: p, seal: s, deps: deps, binding: p.binding, owners: []creationOwned{deps.preflight}}
	c.self = c
	return c, nil
}
func (c *creationSource) enter(ctx context.Context) bool {
	if c == nil || c.self != c || c.stop.Load() || !finite(ctx) || !c.mu.TryLock() {
		return false
	}
	if c.stop.Load() {
		c.mu.Unlock()
		return false
	}
	return true
}
func (c *creationSource) hold(owner creationOwned) {
	if owner != nil {
		c.ownerMu.Lock()
		c.owners = append(c.owners, owner)
		c.ownerMu.Unlock()
	}
}
func (c *creationSource) createDisabled(ctx context.Context, b binding) (r accountReceipt, result error) {
	if !c.enter(ctx) {
		return r, ErrDenied
	}
	defer c.mu.Unlock()
	defer func() {
		if recover() != nil {
			result = ErrUnknown
		}
	}()
	if c.used || b != c.plan.binding {
		return r, ErrDenied
	}
	c.used = true
	if err := c.deps.preflight.check(ctx); err != nil || !finite(ctx) || c.stop.Load() {
		return r, ErrUnknown
	}
	request := creationRequest(b)
	owner, err := c.deps.create()
	c.hold(owner)
	if err != nil || owner == nil || len(request) == 0 || c.stop.Load() || !finite(ctx) {
		return r, ErrUnknown
	}
	output, err := owner.execute(ctx, request)
	if err != nil {
		return r, ErrUnknown
	}
	made, ready, err := decodeCreationResult(c.plan, request, output)
	if err != nil {
		return r, err
	}
	// 確定した作成SIDを、後続constructor・cancelより先に保持する。
	c.binding = made
	r = accountReceipt{c.seal, made, created}
	inspector, inspectErr := c.deps.inspect(made)
	c.hold(inspector)
	if inspectErr != nil || inspector == nil {
		return r, ErrUnconfigured
	}
	cleanup, cleanupErr := newSIDSource(c.plan, made, c.seal, inspector, c.deps.cleanup)
	if cleanupErr != nil {
		return r, cleanupErr
	}
	c.hold(cleanup)
	c.cleanup = cleanup
	if !ready || !finite(ctx) || c.stop.Load() {
		return r, ErrCleanup
	}
	return r, nil
}
func (c *creationSource) inspect(ctx context.Context, b binding) (snapshot accountSnapshot, result error) {
	if !c.enter(ctx) {
		return accountSnapshot{}, ErrDenied
	}
	defer c.mu.Unlock()
	defer func() {
		if recover() != nil {
			c.inspectionUnknown.Store(true)
			snapshot = accountSnapshot{}
			result = ErrUnknown
		}
	}()
	if c.inspectionUnknown.Load() {
		return accountSnapshot{}, ErrUnknown
	}
	if c.cleanup == nil || b != c.binding {
		return accountSnapshot{}, ErrUnknown
	}
	r, err := c.cleanup.inspect(ctx, b)
	if errors.Is(err, ErrUnknown) || errors.Is(err, ErrCleanup) {
		// 既存native診断のfatal/pendingを通常の権限未構成と区別する。
		// 無効staging対象と未知参照を保持し、別workerでnativeを再開しない。
		c.inspectionUnknown.Store(true)
	}
	return r, err
}
func (c *creationSource) disable(ctx context.Context, b binding) (accountReceipt, error) {
	return c.mutate(ctx, b, disabled)
}
func (c *creationSource) deleteExact(ctx context.Context, b binding) (accountReceipt, error) {
	return c.mutate(ctx, b, deleted)
}
func (c *creationSource) mutate(ctx context.Context, b binding, kind accountKind) (accountReceipt, error) {
	if !c.enter(ctx) {
		return accountReceipt{}, ErrDenied
	}
	defer c.mu.Unlock()
	if c.inspectionUnknown.Load() {
		return accountReceipt{}, ErrUnknown
	}
	if c.cleanup == nil || b != c.binding {
		return accountReceipt{}, ErrUnknown
	}
	if kind == disabled {
		return c.cleanup.disable(ctx, b)
	}
	return c.cleanup.deleteExact(ctx, b)
}

// 同じouter ownerから呼び、元call未終了・query fatal・回収不明をackしない。
func (c *creationSource) Close(ctx context.Context) (result error) {
	if c == nil || c.self != c {
		return ErrUnconfigured
	}
	c.stop.Store(true)
	if c.inspectionUnknown.Load() {
		return ErrCleanup
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	if c.mu.TryLock() {
		defer c.mu.Unlock()
	} else {
		result = ErrBusy
	}
	defer func() {
		if recover() != nil {
			result = ErrCleanup
		}
	}()
	if !c.ownerMu.TryLock() {
		return ErrBusy
	}
	owners := append([]creationOwned(nil), c.owners...)
	c.ownerMu.Unlock()
	for _, owner := range owners {
		if owner.Close(ctx) != nil || !finite(ctx) {
			result = ErrCleanup
		}
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	return result
}
