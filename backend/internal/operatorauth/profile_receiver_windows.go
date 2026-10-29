//go:build windows

package operatorauth

import (
	"context"
	"golang.org/x/sys/windows"
)

func nativeProfileReceiverOps() (profileReceiverOps, error) {
	api, err := nativeProfileSyncAPI()
	if err != nil {
		return profileReceiverOps{
			adopt: func(handle uintptr) error {
				kind, err := windows.GetFileType(windows.Handle(handle))
				if err != nil || kind != windows.FILE_TYPE_PIPE || launchHandleAccess(handle, true, 0x120089) != nil {
					return ErrDenied
				}
				if err = windows.SetHandleInformation(windows.Handle(handle), windows.HANDLE_FLAG_INHERIT, 0); err != nil {
					return err
				}
				return launchHandleAccess(handle, false, 0x120089)
			}}, err
	}
	return profileReceiverOps{
		adopt: func(handle uintptr) error {
			kind, err := windows.GetFileType(windows.Handle(handle))
			if err != nil || kind != windows.FILE_TYPE_PIPE || launchHandleAccess(handle, true, 0x120089) != nil {
				return ErrDenied
			}
			if err = windows.SetHandleInformation(windows.Handle(handle), windows.HANDLE_FLAG_INHERIT, 0); err != nil {
				return err
			}
			return launchHandleAccess(handle, false, 0x120089)
		},
		start: func(ctx context.Context, handle uintptr, buffer []byte) (*profileSyncOwner, error) {
			return startProfileSync(ctx, api, profileSyncRead, handle, buffer)
		},
		close: func(handle uintptr) error { return windows.CloseHandle(windows.Handle(handle)) },
	}, nil
}
