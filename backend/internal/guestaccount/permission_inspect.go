package guestaccount

import (
	"context"
	"reflect"
	"sort"
)

// 固定provisioningの保持済み対象だけを借用する。値自体は認証根でない。
// 原handleとmanifestの完全性は外側producerがClose確認まで所有する。
type permissionProfile struct {
	serviceSID string
	objects    []permissionObject
}
type permissionInspection struct {
	profile permissionProfile
	facts   *permissionFactsCollector
	groups  *permissionGroupsCollector
	objects *permissionObjectReader
}
type permissionEvidence struct {
	facts           permissionFacts
	graph           []permissionGroup
	closure, rights []string
	objects         []permissionObjectSnapshot
}

func configurePermissionInspection(q *accountQuery, p permissionProfile, f permissionFactsKernel, g permissionGroupsKernel, o *permissionObjectReader) error {
	if !q.valid() || !q.plan.validBinding(q.binding) || q.permission != nil || !permissionObjectsConfigured(q.binding, p.serviceSID, p.objects) || o.unsafe() || !containsPermissionSID(q.plan.policy.groups, "S-1-5-2") {
		return ErrUnconfigured
	}
	facts, e := newPermissionFactsCollector(q, f)
	if e != nil {
		return e
	}
	groups, e := newPermissionGroupsCollector(q, g)
	if e != nil {
		return e
	}
	p.objects = append([]permissionObject(nil), p.objects...)
	q.permission = &permissionInspection{profile: p, facts: facts, groups: groups, objects: o}
	q.standalone = true
	return nil
}

func (i *permissionInspection) observe(ctx context.Context, q *accountQuery, b binding, flags uint32, guard func() bool) (accountSnapshot, error) {
	first, e := i.once(ctx, q, b, guard)
	if e != nil {
		return accountSnapshot{}, e
	}
	second, e := i.once(ctx, q, b, guard)
	if e != nil || !reflect.DeepEqual(first, second) || first.facts.flags != flags || !finite(ctx) || !guard() {
		return accountSnapshot{}, ErrDenied
	}
	privileges, logons, e := permissionRights(q.plan, first.rights)
	if e != nil {
		return accountSnapshot{}, e
	}
	groups := make([]string, 0, len(first.closure)-1)
	for _, sid := range first.closure {
		if sid != b.sid {
			groups = append(groups, sid)
		}
	}
	if !subset(groups, q.plan.policy.groups) {
		return accountSnapshot{}, ErrDenied
	}
	return accountSnapshot{seal: q.seal, binding: b, presence: exact, complete: true, disabled: true, groups: groups, privileges: privileges, logonRights: logons}, nil
}

func (i *permissionInspection) once(ctx context.Context, q *accountQuery, b binding, guard func() bool) (out permissionEvidence, result error) {
	defer func() {
		if i.objects.unsafe() {
			q.fatal = true
			result = accountQueryScopeFatal{}
		}
	}()
	facts, e := i.facts.user(ctx, b, guard)
	// 初版はdisabled normal USER/primary Users。委譲・信頼・machine・未知flagを許さない。
	const allowed = uint32(0x1 | 0x2 | 0x10 | 0x20 | 0x40 | 0x200 | 0x10000 | 0x100000)
	if e != nil || facts.primary != 513 || facts.priv != 1 || facts.authFlags != 0 || facts.flags&0x202 != 0x202 || facts.flags & ^allowed != 0 {
		return out, ErrDenied
	}
	out.facts = facts
	out.graph, e = i.groups.collect(ctx, b, guard)
	if e != nil {
		return out, e
	}
	for n := range out.graph {
		sort.Strings(out.graph[n].members)
	}
	sort.Slice(out.graph, func(a, b int) bool { return out.graph[a].sid < out.graph[b].sid })
	out.closure, e = permissionClosure(b, out.graph, "S-1-5-2")
	if e != nil {
		return out, e
	}
	for _, trusted := range []string{b.service, b.operator, i.profile.serviceSID, "S-1-5-18", "S-1-5-32-544"} {
		if containsPermissionSID(out.closure, trusted) {
			return out, ErrDenied
		}
	}
	rights := map[string]bool{}
	out.rights = []string{}
	for _, sid := range out.closure {
		assigned, e := i.facts.assigned(ctx, sid, guard)
		if e != nil {
			return out, e
		}
		for _, r := range assigned {
			rights[r] = true
		}
		if len(rights) > 128 {
			return out, ErrDenied
		}
	}
	for r := range rights {
		out.rights = append(out.rights, r)
	}
	sort.Strings(out.rights)
	if _, _, e = permissionRights(q.plan, out.rights); e != nil {
		return out, e
	}
	out.objects = make([]permissionObjectSnapshot, 0, len(i.profile.objects))
	for _, object := range i.profile.objects {
		s, e := i.objects.read(ctx, object, guard)
		if e != nil || !permissionObjectSafe(b, i.profile.serviceSID, s) {
			return out, ErrDenied
		}
		out.objects = append(out.objects, s)
	}
	return out, nil
}
