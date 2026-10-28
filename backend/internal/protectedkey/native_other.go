//go:build !windows

package protectedkey

import "context"

func platformScope(context.Context, Profile) (*NativeScope, error) { return nil, ErrClosed }
