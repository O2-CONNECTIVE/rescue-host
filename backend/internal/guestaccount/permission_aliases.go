package guestaccount

import "context"

type permissionGroupsKernel interface {
	snapshot(context.Context, binding, func() bool) (accountQueryMemory, error)
}
type permissionGroupsCollector struct {
	self   *permissionGroupsCollector
	query  *accountQuery
	kernel permissionGroupsKernel
}

func newPermissionGroupsCollector(q *accountQuery, k permissionGroupsKernel) (*permissionGroupsCollector, error) {
	if !q.valid() || !q.plan.validBinding(q.binding) || k == nil {
		return nil, ErrUnconfigured
	}
	c := &permissionGroupsCollector{query: q, kernel: k}
	c.self = c
	return c, nil
}
func (c *permissionGroupsCollector) collect(ctx context.Context, b binding, guard func() bool) ([]permissionGroup, error) {
	if c == nil || c.self != c || !c.query.valid() || c.query.fatal || c.kernel == nil || b != c.query.binding || !c.query.plan.validBinding(b) || !finite(ctx) || guard == nil || !guard() {
		return nil, ErrDenied
	}
	m, e := c.kernel.snapshot(ctx, b, guard)
	// error/取消を判定する前に、部分取得を同じqueryに一度だけ登録する。
	if m != nil {
		c.query.pending = append(c.query.pending, m)
	}
	if e != nil || m == nil || !finite(ctx) || !guard() {
		return nil, ErrDenied
	}
	o, e := m.decode()
	if e != nil || o.kind != accountQueryGroups || !finite(ctx) || !guard() {
		return nil, ErrDenied
	}
	return copyPermissionAliasGraph(o.groups)
}
func copyPermissionAliasGraph(groups []permissionGroup) ([]permissionGroup, error) {
	if groups == nil || len(groups) > permissionMaxGroups {
		return nil, ErrDenied
	}
	out := make([]permissionGroup, len(groups))
	seen := map[string]bool{}
	edges := 0
	for i, g := range groups {
		if !permissionSID(g.sid) || seen[g.sid] || g.members == nil || len(g.members) > permissionMaxEdges-edges {
			return nil, ErrDenied
		}
		seen[g.sid] = true
		out[i] = permissionGroup{sid: g.sid, members: make([]string, len(g.members))}
		members := map[string]bool{}
		for j, sid := range g.members {
			edges++
			if edges > permissionMaxEdges || !permissionSID(sid) || members[sid] {
				return nil, ErrDenied
			}
			members[sid] = true
			out[i].members[j] = sid
		}
	}
	return out, nil
}
