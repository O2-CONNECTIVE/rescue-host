package guestaccount

import (
	"context"
	"errors"
	"testing"
)

func TestCloseDuringCreateClosesAdmissionAndRetainsLateOwner(t *testing.T) {
	l, f := newFixture(t)
	f.createEntered = make(chan struct{})
	f.createRelease = make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(f.createRelease)
		}
	}()
	result := make(chan error, 1)
	ctx := testContext(t)
	go func() { result <- l.Prepare(ctx) }()
	<-f.createEntered
	if err := l.Close(testContext(t)); !errors.Is(err, ErrBusy) {
		t.Fatalf("未終了ownerへのclose=%v", err)
	}
	busy, err := l.Status()
	if !errors.Is(err, ErrBusy) || !busy.AdmissionClosed || busy.RevocationConfirmed {
		t.Fatalf("busyは実失効成功でない: %+v %v", busy, err)
	}
	close(f.createRelease)
	released = true
	if err := <-result; !errors.Is(err, ErrCleanup) {
		t.Fatalf("late createがpreparedへ進んだ=%v", err)
	}
	s, _ := l.Status()
	if s.SID == "" || s.State == Prepared || !s.CleanupRequired {
		t.Fatalf("late owner=%+v", s)
	}
	if err := l.Prepare(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatalf("停止後admission=%v", err)
	}
	if err := l.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredCloseStillClosesLocalAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(testContext(t))
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), canceled} {
		l, f := newFixture(t)
		if err := l.Close(ctx); !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
		if err := l.Prepare(testContext(t)); !errors.Is(err, ErrDenied) {
			t.Fatalf("budget不正close後prepare=%v", err)
		}
		if len(f.trace) != 0 {
			t.Fatalf("予算なしsource実行=%v", f.trace)
		}
		s, _ := l.Status()
		if !s.AdmissionClosed || s.RevocationConfirmed {
			t.Fatalf("local admissionと実失効混同=%+v", s)
		}
	}
}

func TestConcurrentCloseNeverDeletesWhileSourceOwnerIsInFlight(t *testing.T) {
	l, f := preparedFixture(t)
	f.quiesceEntered = make(chan struct{})
	f.quiesceRelease = make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(f.quiesceRelease)
		}
	}()
	ctx := testContext(t)
	result := make(chan error, 1)
	go func() { result <- l.Close(ctx) }()
	<-f.quiesceEntered
	if err := l.Close(testContext(t)); !errors.Is(err, ErrBusy) {
		t.Fatalf("同時close=%v", err)
	}
	close(f.quiesceRelease)
	released = true
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, step := range f.trace {
		if step == "delete" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("重複delete=%v", f.trace)
	}
}
