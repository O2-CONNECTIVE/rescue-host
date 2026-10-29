package guestaccount

import (
	"errors"
	"testing"
)

type creatorSessionFixture struct {
	*fixture
	receipt     accountReceipt
	accountSeal *seal
	bound       int
}

func (s *creatorSessionFixture) bindCreated(receipt accountReceipt, expected *seal) error {
	s.receipt, s.accountSeal = receipt, expected
	s.bound++
	return nil
}

// The existing bounded account/session OS model is used. This tests only the
// real creator-receipt handoff; it creates no authentication or runtime proof.
func TestCreatorReceiptBindsSessionBeforePartialCreateError(t *testing.T) {
	l, f := newFixture(t)
	session := &creatorSessionFixture{fixture: f}
	l.sources.session = session
	f.partial = true
	f.fail["create"] = errors.New("partial creator outcome")
	if err := l.Prepare(testContext(t)); err == nil {
		t.Fatal("partial creation succeeded")
	}
	if session.bound != 1 || session.accountSeal != l.sources.accountSeal || session.receipt.seal != l.sources.accountSeal || session.receipt.binding != l.record.binding || session.receipt.kind != created || session.receipt.binding.sid == "" {
		t.Fatal("actual partial creator receipt was not bound to the same session")
	}
}
