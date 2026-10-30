//go:build windows

package childjob

import (
	"runtime"
	"testing"

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
