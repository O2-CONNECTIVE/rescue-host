package guestaccount

import (
	"context"
	"strings"
	"unicode/utf8"
)

// permissionFactsPhase は照会の取消と、回収にも使う raw scope を分離する。
type permissionFactsPhase struct {
	ctx   context.Context
	guard func() bool
}

func (p permissionFactsPhase) allowed() bool {
	return finite(p.ctx) && p.guard != nil && p.guard() && finite(p.ctx)
}

type permissionFacts struct{ primary, flags, priv, authFlags uint32 }
type permissionFactsKernel interface {
	user4(binding, permissionFactsPhase) (accountQueryMemory, error)
	rights(string, permissionFactsPhase) (accountQueryMemory, error)
}
type permissionFactsCollector struct {
	self   *permissionFactsCollector
	query  *accountQuery
	kernel permissionFactsKernel
}

func newPermissionFactsCollector(q *accountQuery, k permissionFactsKernel) (*permissionFactsCollector, error) {
	if !q.valid() || !q.plan.validBinding(q.binding) || k == nil {
		return nil, ErrUnconfigured
	}
	c := &permissionFactsCollector{query: q, kernel: k}
	c.self = c
	return c, nil
}
func (c *permissionFactsCollector) valid() bool {
	return c != nil && c.self == c && c.query.valid() && !c.query.fatal && c.query.plan.validBinding(c.query.binding) && c.kernel != nil
}
func (c *permissionFactsCollector) user(ctx context.Context, b binding, guard func() bool) (permissionFacts, error) {
	if !c.valid() || b != c.query.binding {
		return permissionFacts{}, ErrDenied
	}
	o, e := c.read(ctx, b, "", accountQueryUser4, guard)
	if e != nil || o.name != b.name || o.sid != b.sid {
		return permissionFacts{}, ErrDenied
	}
	return permissionFacts{primary: o.primary, flags: o.flags, priv: o.priv, authFlags: o.authFlags}, nil
}
func (c *permissionFactsCollector) assigned(ctx context.Context, sid string, guard func() bool) ([]string, error) {
	if !c.valid() {
		return nil, ErrDenied
	}
	if _, e := permissionFactsSID(sid); e != nil {
		return nil, ErrDenied
	}
	o, e := c.read(ctx, c.query.binding, sid, accountQueryRights, guard)
	if e != nil || o.sid != sid {
		return nil, ErrDenied
	}
	return copyPermissionRights(o.rights)
}
func (c *permissionFactsCollector) read(ctx context.Context, b binding, sid string, kind accountQueryKind, guard func() bool) (accountQueryObservation, error) {
	if !finite(ctx) || guard == nil || !guard() {
		return accountQueryObservation{}, ErrDenied
	}
	var m accountQueryMemory
	var e error
	if kind == accountQueryUser4 {
		m, e = c.kernel.user4(b, permissionFactsPhase{ctx: ctx, guard: guard})
	} else {
		m, e = c.kernel.rights(sid, permissionFactsPhase{ctx: ctx, guard: guard})
	}
	// 部分 return は error/cancel/scope の判定より先に元 query へ残す。
	if m != nil {
		c.query.pending = append(c.query.pending, m)
	}
	if e != nil || m == nil || !finite(ctx) || !guard() {
		return accountQueryObservation{}, ErrDenied
	}
	o, e := m.decode()
	if e != nil || o.kind != kind || !finite(ctx) || !guard() {
		return accountQueryObservation{}, ErrDenied
	}
	return o, nil
}
func copyPermissionRights(values []string) ([]string, error) {
	if values == nil || len(values) > 256 {
		return nil, ErrDenied
	}
	out := make([]string, len(values))
	seen := map[string]bool{}
	for i, v := range values {
		if len(v) == 0 || len(v) > 184 || !utf8.ValidString(v) || strings.ContainsAny(v, "\x00\r\n") || seen[v] {
			return nil, ErrDenied
		}
		seen[v] = true
		out[i] = strings.Clone(v)
	}
	return out, nil
}
