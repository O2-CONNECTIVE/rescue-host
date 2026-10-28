//go:build !windows

package childjob

import "context"

func platformStart(context.Context, Spec) (*Child, error) { return nil, ErrUnsupported }
