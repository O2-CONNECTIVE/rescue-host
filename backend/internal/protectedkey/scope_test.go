package protectedkey

import (
	"context"
	"testing"
)

type memoryScopeKernel struct {
	*memoryKernel
	thread uint32
}

func (m *memoryScopeKernel) threadID() uint32 { return m.thread }

func TestLoadUsesOpaqueScopeOnOwningThread(t *testing.T) {
	_, memory := protectedMemoryLoader(t)
	api := &memoryScopeKernel{memoryKernel: memory, thread: 42}
	profile := provisionedProfile()
	scope, err := newNativeScope(loadContext(t), profile, api)
	if err != nil {
		t.Fatal(err)
	}
	loader, err := NewNative(profile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := loader.Load(scope.Context(), testRef)
	if err != nil || signer == nil {
		t.Fatalf("scoped load failed: %v", err)
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if signer, err := loader.Load(scope.Context(), testRef); signer != nil || err != ErrFatal {
		t.Fatal("closed scope reused")
	}
}

func TestScopeDoesNotAuthorizeUnscopedContext(t *testing.T) {
	loader, err := NewNative(provisionedProfile())
	if err != nil {
		t.Fatal(err)
	}
	if signer, err := loader.Load(context.Background(), testRef); signer != nil || err != ErrDenied {
		t.Fatal("context without deadline accepted")
	}
}

func TestScopePoisonCannotBeRecoveredByRestoringPrincipal(t *testing.T) {
	_, memory := protectedMemoryLoader(t)
	api := &memoryScopeKernel{memoryKernel: memory, thread: 42}
	profile := provisionedProfile()
	scope, err := newNativeScope(loadContext(t), profile, api)
	if err != nil {
		t.Fatal(err)
	}
	loader, err := NewNative(profile)
	if err != nil {
		t.Fatal(err)
	}
	api.principalValue.threadToken = true
	if signer, err := loader.Load(scope.Context(), testRef); signer != nil || err != ErrFatal {
		t.Fatal("dirty scope accepted")
	}
	api.principalValue.threadToken = false
	if signer, err := loader.Load(scope.Context(), testRef); signer != nil || err != ErrFatal {
		t.Fatal("poisoned scope revived")
	}
	if err := scope.Close(); err != ErrFatal {
		t.Fatal("poisoned scope closed successfully")
	}
	if api.held != 0 {
		t.Fatal("dirty entry opened a key")
	}
}

func TestScopeRejectsThreadSwitchAndProfileMismatch(t *testing.T) {
	for _, mismatch := range []string{"thread", "profile"} {
		t.Run(mismatch, func(t *testing.T) {
			_, memory := protectedMemoryLoader(t)
			api := &memoryScopeKernel{memoryKernel: memory, thread: 42}
			profile := provisionedProfile()
			scope, err := newNativeScope(loadContext(t), profile, api)
			if err != nil {
				t.Fatal(err)
			}
			if mismatch == "thread" {
				api.thread++
			} else {
				profile.Provisioning.Reference = "other-provisioning-record"
			}
			loader, err := NewNative(profile)
			if err != nil {
				t.Fatal(err)
			}
			if signer, err := loader.Load(scope.Context(), testRef); signer != nil || err != ErrFatal {
				t.Fatal("mismatched scope accepted")
			}
			if err := scope.Close(); err != ErrFatal {
				t.Fatal("mismatched scope recovered")
			}
			if api.held != 0 {
				t.Fatal("mismatched scope opened a key")
			}
		})
	}
}

func TestScopeCloseDetectsPrincipalChange(t *testing.T) {
	_, memory := protectedMemoryLoader(t)
	api := &memoryScopeKernel{memoryKernel: memory, thread: 42}
	scope, err := newNativeScope(loadContext(t), provisionedProfile(), api)
	if err != nil {
		t.Fatal(err)
	}
	api.principalErr = ErrDenied
	if err := scope.Close(); err != ErrFatal {
		t.Fatal("unknown principal accepted during close")
	}
	api.principalErr = nil
	if err := scope.Close(); err != ErrFatal {
		t.Fatal("failed close revived")
	}
}
