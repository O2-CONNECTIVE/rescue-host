//go:build windows

package childjob

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Read-only CI observation of this test process, not a guest logon or runtime
// acceptance. Never adjusts a token or account and never prints observed SIDs.
func TestNativeGuestTokenReaderQueriesCurrentProcessWithoutGrant(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var token windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token) != nil {
		t.Fatal("current-process query token unavailable")
	}
	defer func() {
		if token.Close() != nil {
			t.Error("query token close unconfirmed")
		}
	}()
	facts, err := (windowsKernel{}).tokenFacts(uintptr(token))
	if err != nil || facts.kind != 1 || facts.user == "" || facts.tokenID == 0 || facts.modifiedID == 0 || facts.groups == nil || facts.privileges == nil {
		diagnoseGuestTokenRead(t, token)
		t.Fatal("native bounded token read was not complete")
	}
	different := "S-1-5-21-7-8-9-2048"
	if facts.user == different {
		different = "S-1-5-21-7-8-9-2049"
	}
	expectation, err := NewGuestTokenExpectation(different, []string{"S-1-1-0", "S-1-5-2", "S-1-5-11", "S-1-5-32-545"}, []string{})
	if err != nil || expectation.accepts(facts) {
		t.Fatal("self query or caller expectation manufactured guest authority")
	}
}

// Failure-only fixed-stage diagnostics. Never format OS values or native errors.
func diagnoseGuestTokenRead(t *testing.T, token windows.Token) {
	t.Helper()
	check := func(stage string, ok bool) {
		if !ok {
			t.Errorf("read-only token query stage failed: %s", stage)
		}
	}
	_, err := tokenType(windows.GetCurrentThreadToken())
	check("thread-before", err == windows.ERROR_NO_TOKEN)
	before, err := targetPrincipalOf(token)
	check("principal-before", err == nil)
	for _, field := range []struct {
		stage string
		class uint32
		size  int
	}{{"owner", windows.TokenOwner, 8}, {"primary-group", windows.TokenPrimaryGroup, 8}, {"integrity", windows.TokenIntegrityLevel, 16}} {
		_, err = guestTokenSID(token, field.class, field.size)
		check(field.stage, err == nil)
	}
	for _, field := range []struct {
		stage string
		class uint32
	}{{"elevation-type", windows.TokenElevationType}, {"elevation", windows.TokenElevation}, {"ui-access", windows.TokenUIAccess}, {"restrictions", windows.TokenHasRestrictions}, {"app-container", guestTokenIsAppContainer}} {
		_, err = guestTokenDWORD(token, field.class)
		check(field.stage, err == nil)
		if field.class == windows.TokenHasRestrictions && err != nil {
			var buffer, returned uint32
			nativeErr := windows.GetTokenInformation(token, field.class, (*byte)(unsafe.Pointer(&buffer)), 4, &returned)
			shape := "other-length"
			if returned == 1 {
				shape = "one-byte"
			} else if returned == 4 {
				shape = "four-bytes"
			}
			t.Logf("restriction query layout: success=%t shape=%s", nativeErr == nil, shape)
		}
	}
	err = withGuestTokenInfo(token, windows.TokenGroups, 65536, func(data []byte, base uint64) error {
		_, e := guestTokenGroups(data, base)
		return e
	})
	check("groups", err == nil)
	names, err := guestPrivilegeLUIDs()
	check("privilege-names", err == nil)
	err = withGuestTokenInfo(token, windows.TokenPrivileges, 1024, func(data []byte, _ uint64) error {
		_, e := guestTokenPrivileges(data, names)
		return e
	})
	check("privileges", err == nil)
	after, err := targetPrincipalOf(token)
	check("principal-after", err == nil && before == after)
	_, err = tokenType(windows.GetCurrentThreadToken())
	check("thread-after", err == windows.ERROR_NO_TOKEN)
}
