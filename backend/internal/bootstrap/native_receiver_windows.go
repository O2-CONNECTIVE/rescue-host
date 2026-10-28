//go:build windows

package bootstrap

import (
	"context"
	"golang.org/x/sys/windows"
)

// Authenticate 成功後に所有した handle のみを扱います。未検証 argv は渡しません。
func closeAuthenticatedReadHandle(ctx context.Context, h uintptr) error {
	if !receiverLive(ctx) {
		return ErrNativeUnconfirmed
	}
	return windows.CloseHandle(windows.Handle(h))
}
