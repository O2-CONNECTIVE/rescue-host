package guestaccount

import "testing"

func TestRuntimeGuestRefusesMissingSecretWriterBeforeAccountOrSecretAcquisition(t *testing.T) {
	r, f := runtimeOwnerFixture(t)
	r.ops.credential = func() runtimeCredential { return &runtimeCredentialFixture{f: f, prepareError: ErrUnconfigured} }
	if _, err := r.Prepare(testContext(t)); err == nil {
		t.Fatal("unconfigured secret writer accepted")
	}
	if r.created.sid != "" || r.secret != nil || r.rights != nil || r.token != nil {
		t.Fatal("secret or account acquired before secret-writer configuration")
	}
}
