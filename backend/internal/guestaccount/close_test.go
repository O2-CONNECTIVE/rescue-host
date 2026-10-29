package guestaccount

import (
	"reflect"
	"testing"
)

func TestCloseRevokesAndQuiescesBeforeExactDeletion(t *testing.T) {
	l, f := newFixture(t)
	if err := l.Prepare(testContext(t)); err != nil {
		t.Fatal(err)
	}
	f.trace = nil
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	s, err := l.Status()
	if err != nil || s.State != Closed || s.CleanupRequired {
		t.Fatalf("status=%+v error=%v", s, err)
	}
	want := []string{"revoke", "disable", "quiesce", "inspect", "commit:5", "inspect", "delete", "inspect", "commit:6"}
	if !reflect.DeepEqual(f.trace, want) {
		t.Fatalf("安全な停止順序=%v want=%v", f.trace, want)
	}
	f.trace = nil
	if err := l.Close(testContext(t)); err != nil || len(f.trace) != 0 {
		t.Fatalf("清掃完了後の再操作=%v %v", err, f.trace)
	}
}
