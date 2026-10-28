package protectedkey

import (
	"context"
	"io"
	"reflect"
)

type tokenGroup struct {
	sid        string
	attributes uint32
}
type principal struct {
	userSID     string
	tokenType   uint32
	groups      []tokenGroup
	threadToken bool
}

type snapshot struct {
	path, owner, filesystem               string
	acl                                   []ACE
	daclPresent, disk, directory, reparse bool
	volume                                uint64
	id                                    [16]byte
	links                                 uint32
	size                                  int64
}

type protectedFile interface {
	io.Reader
	inspect() (snapshot, error)
	close() error
}
type kernel interface {
	principal(context.Context) (principal, error)
	open(context.Context, string, bool) (protectedFile, error)
}

type securedSource struct {
	profile Profile
	api     kernel
}

func (s *securedSource) read(ctx context.Context, key KeyProfile) (data []byte, result error) {
	if err := s.checkPrincipal(ctx); err != nil {
		return nil, err
	}
	var handles []protectedFile
	var before []snapshot
	defer func() {
		for i := len(handles) - 1; i >= 0; i-- {
			if handles[i].close() != nil {
				result = ErrFatal
			}
		}
		if s.checkPrincipal(context.Background()) != nil {
			result = ErrFatal
		}
		if result != nil {
			clear(data)
			data = nil
		}
	}()
	for i, node := range key.Nodes {
		handle, err := s.api.open(ctx, node.Path, i == len(key.Nodes)-1)
		if err != nil || handle == nil {
			return nil, ErrDenied
		}
		handles = append(handles, handle)
		state, err := handle.inspect()
		if err != nil || !s.validSnapshot(state, node, i == len(key.Nodes)-1) {
			return nil, ErrDenied
		}
		before = append(before, state)
	}
	buffer := make([]byte, 16*1024+1)
	defer func() {
		if result != nil {
			clear(buffer)
		}
	}()
	n, err := io.ReadFull(handles[len(handles)-1], buffer)
	if err != io.EOF && err != io.ErrUnexpectedEOF || n > 16*1024 {
		return nil, ErrDenied
	}
	if int64(n) != before[len(before)-1].size {
		return nil, ErrDenied
	}
	for i, handle := range handles {
		state, err := handle.inspect()
		if err != nil || !s.validSnapshot(state, key.Nodes[i], i == len(handles)-1) || !reflect.DeepEqual(state, before[i]) {
			return nil, ErrDenied
		}
	}
	return buffer[:n], nil
}

func (s *securedSource) validSnapshot(state snapshot, expected NodePolicy, leaf bool) bool {
	if !state.disk || state.reparse || state.path != expected.Path || state.owner != expected.OwnerSID || !trusted(s.profile, state.owner) || !state.daclPresent || !safeACL(s.profile, state.acl, leaf) || !reflect.DeepEqual(state.acl, expected.ACL) || state.volume != s.profile.Volume.Serial || state.filesystem != s.profile.Volume.Filesystem || state.id == [16]byte{} || state.directory == leaf {
		return false
	}
	return !leaf || state.links == 1 && state.size > 0 && state.size <= 16*1024
}

func (s *securedSource) checkPrincipal(ctx context.Context) error {
	p, err := s.api.principal(ctx)
	if err != nil || p.threadToken || p.userSID != s.profile.ProcessUserSID || p.tokenType != 1 {
		return ErrFatal
	}
	for _, group := range p.groups {
		if group.sid == s.profile.ServiceSID && group.attributes&4 != 0 && group.attributes&0x10 == 0 {
			return nil
		}
	}
	return ErrFatal
}
