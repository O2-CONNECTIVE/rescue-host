//go:build !windows

package childjob

import "context"

func platformStart(context.Context, Spec) (*Child, error)      { return nil, ErrUnsupported }
func platformPrepare(context.Context, Spec) (*Prepared, error) { return nil, ErrUnsupported }
