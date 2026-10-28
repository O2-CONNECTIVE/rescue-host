package lease

import (
	"strings"
	"testing"
	"time"
)

func TestCloseoutN6FourReservations(t *testing.T) {
	id := Identity{"four", "epoch", strings.Repeat("a", 64)}
	a, err := New(id, time.Second, time.Second, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { a.Revoke(id); <-a.Done() }()
	a.Transition(id, Prepared, Ready)
	a.Transition(id, Ready, Active)
	var all []*Reservation
	for n := 0; n < 4; n++ {
		r, err := a.Reserve()
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	if _, err := a.Reserve(); err == nil {
		t.Fatal("pending5件目")
	}
	for _, r := range all {
		if r.Promote() != nil {
			t.Fatal("promote失敗")
		}
	}
	if _, err := a.Reserve(); err == nil {
		t.Fatal("active5件目")
	}
	a.Revoke(id)
	for _, r := range all {
		r.Release()
	}
	if a.Terminate() != nil {
		t.Fatal("4枠回収失敗")
	}
}
