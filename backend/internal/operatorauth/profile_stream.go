package operatorauth

import (
	"bytes"
	"sync"
)

// profileStreamは配送層が確認したchunkとEOFだけを消費する純粋状態。
// eofの値自体はnative pipeの真正性やOS上のEOFの証明ではない。
type profileStream struct {
	mu                      sync.Mutex
	expected                string
	frame                   []byte
	profile                 helperProfile
	ready, failed, consumed bool
}

func newProfileStream(expected string) *profileStream { return &profileStream{expected: expected} }
func (s *profileStream) accept(chunk []byte, eof bool) (bool, error) {
	if s == nil {
		return false, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.ready || s.consumed {
		s.failed = true
		return false, ErrDenied
	}
	if !profileLaunchID(s.expected) || (eof && len(chunk) != 0) || (!eof && len(chunk) == 0) || len(chunk) > 16384-len(s.frame) {
		s.failed = true
		return false, ErrDenied
	}
	if !eof {
		if bytes.IndexByte(s.frame, '\n') >= 0 || (bytes.IndexByte(chunk, '\n') >= 0 && bytes.IndexByte(chunk, '\n') != len(chunk)-1) {
			s.failed = true
			return false, ErrDenied
		}
		s.frame = append(s.frame, chunk...)
		return false, nil
	}
	profile, err := decodeHelperProfileFrame(s.frame, s.expected)
	if err != nil {
		s.failed = true
		return false, ErrDenied
	}
	s.profile, s.ready = profile, true
	return true, nil
}
func (s *profileStream) consume() (helperProfile, error) {
	if s == nil {
		return helperProfile{}, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed || s.failed || !s.ready {
		s.failed = true
		return helperProfile{}, ErrDenied
	}
	s.consumed = true
	return s.profile, nil
}
