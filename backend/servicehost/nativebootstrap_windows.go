//go:build windows

package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"golang.org/x/sys/windows"
)

func closeNativeChildPipes(ctx context.Context, h *childjob.Handles, token uintptr) error {
	for _, handle := range []*uintptr{&h.Stdin, &h.Stdout, &h.Stderr} {
		if *handle == 0 {
			continue
		}
		if !ordinaryNativeHandle(*handle) || *handle == token || ctx.Err() != nil {
			return ErrRuntimeUnconfirmed
		}
		kind, err := windows.GetFileType(windows.Handle(*handle))
		if err != nil || kind != windows.FILE_TYPE_PIPE {
			return ErrRuntimeUnconfirmed
		}
		if windows.CloseHandle(windows.Handle(*handle)) != nil {
			return ErrRuntimeUnconfirmed
		}
		closed := *handle
		*handle = 0
		// 部分供給のaliasがあっても同じhandleを二度閉じません。
		for _, other := range []*uintptr{&h.Stdin, &h.Stdout, &h.Stderr} {
			if *other == closed {
				*other = 0
			}
		}
	}
	return nil
}
