package guestaccount

import (
	"context"
	"crypto/rand"
	"runtime"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
)

type runtimeAuthority interface {
	bind(context.Context, binding) error
	revoke(context.Context, binding) error
}
type runtimeStage interface {
	Prepare(context.Context) error
	Close(context.Context) error
}
type runtimeRights interface {
	provision(context.Context, binding, []string, func() bool) error
	Close(context.Context) error
}
type runtimeCredential interface {
	prepare(context.Context, func() bool) error
	provision(context.Context, binding, []uint16, func() bool) error
	Close(context.Context) error
}
type runtimeToken interface {
	acquire(context.Context, binding, []uint16, func() bool) error
	inspect(context.Context, binding, func() bool) error
	value() uintptr
	Close(context.Context) error
}
type runtimeTarget interface {
	prepare(context.Context, binding, uintptr, childjob.Spec, func() bool) (*childjob.Prepared, error)
	Close(context.Context) error
}

// Only a concrete native constructor chooses these factories; fixtures are private.
type runtimeOps struct {
	scope      func() bool // original cleanup subject; admission additionally checks stop
	random     func([]byte) error
	rights     func() runtimeRights
	credential func() runtimeCredential
	token      func() runtimeToken
	target     func() runtimeTarget
	check      func(context.Context, binding) error
}

func runtimeRandom(b []byte) error { _, err := rand.Read(b); return err }
func runtimeSecret(fill func([]byte) error) ([]uint16, error) {
	var entropy [32]byte
	defer func() { clear(entropy[:]); runtime.KeepAlive(&entropy) }()
	if fill == nil || fill(entropy[:]) != nil {
		return nil, ErrDenied
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	s := make([]uint16, 37)
	s[0], s[1], s[2], s[3] = 'A', 'a', '1', '!'
	for i, v := range entropy {
		s[i+4] = uint16(alphabet[v&63])
	}
	return s, nil
}
func clearRuntimeSecret(s []uint16) { clear(s); runtime.KeepAlive(s) }
