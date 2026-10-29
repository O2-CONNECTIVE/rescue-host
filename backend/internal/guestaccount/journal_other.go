//go:build !windows

package guestaccount

import "context"

func newNativeJournal(context.Context, Plan, *journalRootCapability) (*journalBackend, error) {
	return nil, ErrUnconfigured
}
