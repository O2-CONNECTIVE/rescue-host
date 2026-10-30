package guestaccount

import (
	"context"
	"slices"
	"sort"
)

type runtimeRightsKernel interface {
	read(context.Context, binding, func() bool) ([]string, error)
	add(context.Context, binding, []string, func() bool) error
	remove(context.Context, binding, []string, func() bool) error
	close(context.Context, func() bool) error
}
type runtimeRightsOwner struct {
	kernel                           runtimeRightsKernel
	binding                          binding
	scope                            func() bool
	before, added                    []string
	used, attempted, removed, closed bool
}

func newRuntimeRightsOwner(k runtimeRightsKernel, scope func() bool) *runtimeRightsOwner {
	return &runtimeRightsOwner{kernel: k, scope: scope}
}
func (r *runtimeRightsOwner) provision(ctx context.Context, b binding, want []string, guard func() bool) error {
	if r == nil || r.kernel == nil || r.scope == nil || r.used || guard == nil || !guard() || !finite(ctx) || !accountSID(b.sid) || !labels(want, true) {
		return ErrDenied
	}
	r.used, r.binding = true, b
	before, err := r.kernel.read(ctx, b, guard)
	if err != nil || !finite(ctx) || !guard() || before == nil || !labels(before, true) {
		return ErrDenied
	}
	r.before = append([]string{}, before...)
	sort.Strings(r.before)
	for _, right := range want {
		if !containsPermissionSID(before, right) {
			r.added = append(r.added, right)
		}
	}
	if len(r.added) == 0 {
		return nil
	}
	// Retain the full intended difference before the native side effect. An error
	// may be a partial write and must never erase cleanup responsibility.
	r.attempted = true
	if err = r.kernel.add(ctx, b, r.added, guard); err != nil || !finite(ctx) || !guard() {
		return ErrUnknown
	}
	after, err := r.kernel.read(ctx, b, guard)
	if err != nil || after == nil || !labels(after, true) || !finite(ctx) || !guard() {
		return ErrUnknown
	}
	expected := append(append([]string{}, r.before...), r.added...)
	sort.Strings(expected)
	sort.Strings(after)
	if !slices.Equal(expected, after) {
		return ErrUnknown
	}
	return nil
}
func (r *runtimeRightsOwner) Close(ctx context.Context) error {
	if r == nil || r.closed {
		return nil
	}
	if !r.used {
		return nil
	}
	if !finite(ctx) || r.scope == nil || !r.scope() {
		return ErrCleanup
	}
	if r.attempted && !r.removed {
		current, err := r.kernel.read(ctx, r.binding, r.scope)
		if err != nil || current == nil || !labels(current, true) || !finite(ctx) || !r.scope() {
			return ErrCleanup
		}
		remaining := []string{}
		for _, right := range r.added {
			if containsPermissionSID(current, right) {
				remaining = append(remaining, right)
			}
		}
		if len(remaining) > 0 {
			if err := r.kernel.remove(ctx, r.binding, remaining, r.scope); err != nil || !finite(ctx) || !r.scope() {
				return ErrCleanup
			}
		}
		after, err := r.kernel.read(ctx, r.binding, r.scope)
		if err != nil || after == nil || !labels(after, true) || !finite(ctx) || !r.scope() {
			return ErrCleanup
		}
		sort.Strings(after)
		if !slices.Equal(r.before, after) {
			return ErrCleanup
		}
		r.removed = true
	}
	if err := r.kernel.close(ctx, r.scope); err != nil || !finite(ctx) {
		return ErrCleanup
	}
	r.closed = true
	return nil
}
