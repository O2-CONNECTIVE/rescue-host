//go:build windows

package guestaccount

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

func runtimePlatform(r *RuntimeGuest) (runtimeOps, error) {
	guard := r.nativeScope
	return runtimeOps{scope: guard, random: runtimeRandom,
		rights: func() runtimeRights { return newRuntimeRightsOwner(&runtimeRightsWindows{}, guard) },
		credential: func() runtimeCredential {
			s, ok := r.stage.(*stagingOwner)
			if !ok || s == nil {
				return &runtimeCredentialExecutor{}
			}
			owner, _ := runtimeCredentialNative(s.profile.native)
			return &runtimeCredentialExecutor{owner: owner, admission: func() bool { return !r.stop.Load() }}
		},
		token: func() runtimeToken {
			s, _ := r.stage.(*stagingOwner)
			return &runtimeTokenWindows{serviceSID: s.profile.serviceSID, scope: guard}
		},
		target: func() runtimeTarget {
			s, _ := r.stage.(*stagingOwner)
			return &runtimeTargetWindows{serviceSID: s.profile.serviceSID, user: r.plan.binding.service, scope: guard}
		},
		check: func(ctx context.Context, b binding) error {
			if !r.acquireScope() {
				return ErrDenied
			}
			s, err := r.lifecycle.sources.accounts.inspect(ctx, b)
			if err != nil || !r.lifecycle.limited(s) || !guard() || !finite(ctx) {
				return ErrDenied
			}
			for _, right := range r.plan.policy.logonRights {
				if !containsPermissionSID(s.logonRights, right) {
					return ErrDenied
				}
			}
			return nil
		},
	}, nil
}
func runtimeCredentialNative(p sidNativeProfile) (*sidRunOwner, error) {
	command, err := childjob.NewFixedCommand([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", runtimeCredentialScript(p.manifest.Path)}, []childjob.Artifact{p.manifest, p.assembly})
	if err != nil {
		return nil, ErrUnconfigured
	}
	return newNativeSIDExecutor(sidRunConfig{spec: childjob.Spec{Artifact: p.image, PrimaryToken: p.token, CleanupTimeout: p.stdio.CleanupTimeout, Command: command}, stdio: p.stdio})
}

type runtimeTargetWindows struct {
	serviceSID, user string
	pipes            *childstdio.Pipes
	prepared         *childjob.Prepared
	scope            func() bool
	closed           bool
}

func (t *runtimeTargetWindows) prepare(ctx context.Context, b binding, token uintptr, spec childjob.Spec, guard func() bool) (*childjob.Prepared, error) {
	if t.pipes != nil || t.prepared != nil || t.closed || t.scope == nil || !finite(ctx) || guard == nil || !guard() || spec.PrimaryToken != 0 || spec.Handles != (childjob.Handles{}) || spec.Protection != nil {
		return nil, ErrDenied
	}
	protection, err := childjob.NewProtectedGuestConfig(t.serviceSID, b.sid)
	if err != nil {
		return nil, err
	}
	t.pipes, err = childstdio.NewNative(ctx, childstdio.Setup{UserSID: t.user, ServiceSID: t.serviceSID, CleanupTimeout: spec.CleanupTimeout})
	if err != nil || t.pipes == nil || !finite(ctx) || !guard() {
		return nil, ErrDenied
	}
	spec.PrimaryToken, spec.Handles, spec.Protection = token, t.pipes.ChildHandles(), &protection
	t.prepared, err = childjob.Prepare(ctx, spec)
	if err != nil || t.prepared == nil || !finite(ctx) || !guard() {
		return t.prepared, ErrDenied
	}
	if err = t.pipes.ReleaseChild(ctx); err != nil || !finite(ctx) || !guard() {
		return t.prepared, ErrDenied
	}
	return t.prepared, nil
}
func (t *runtimeTargetWindows) Close(ctx context.Context) error {
	if t.closed {
		return nil
	}
	if t.pipes == nil && t.prepared == nil {
		t.closed = true
		return nil
	}
	if !finite(ctx) || t.scope == nil || !t.scope() {
		return ErrCleanup
	}
	if t.prepared != nil {
		if err := t.prepared.Close(ctx); err != nil {
			return err
		}
	}
	if t.pipes != nil {
		if err := t.pipes.Close(ctx); err != nil {
			return err
		}
	}
	if !finite(ctx) || !t.scope() {
		return ErrCleanup
	}
	t.closed = true
	return nil
}
