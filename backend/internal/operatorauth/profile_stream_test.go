package operatorauth

import (
	"strings"
	"testing"
)

func TestProfileStreamRequiresEOFSentinelAndBurnsOnFailure(t *testing.T) {
	id := strings.Repeat("a", 64)
	frame := []byte(`{"version":"1","type":"helper-profile","launchId":"` + id + `","operator":{"user":"S-1-5-21-1","authentication":"7","session":"3","integrity":"8192","kind":"2","level":"1","restricted":false,"appContainer":false},"guestUser":"S-1-5-21-2"}` + "\n")
	s := newProfileStream(id)
	if _, err := s.accept(frame[:10], false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accept(frame[10:], false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.consume(); err == nil {
		t.Fatal("frame without EOF accepted")
	}
	if _, err := s.accept(nil, true); err == nil {
		t.Fatal("failed stream revived")
	}

	s = newProfileStream(id)
	if _, err := s.accept(frame, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accept(nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.consume(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.consume(); err == nil {
		t.Fatal("profile reused")
	}

	s = newProfileStream(id)
	if _, err := s.accept(frame, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accept([]byte("x"), false); err == nil {
		t.Fatal("bytes after frame accepted")
	}
	s = newProfileStream(id)
	if _, err := s.accept(frame, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accept(nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accept([]byte("x"), false); err == nil {
		t.Fatal("bytes after EOF accepted")
	}
	if _, err := s.consume(); err == nil {
		t.Fatal("stream revived after trailing input")
	}
}
