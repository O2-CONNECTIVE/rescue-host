//go:build windows

package operatorauth

import (
	"context"

	"golang.org/x/sys/windows"
)

type winProfilePipeKernel struct{}

func newNativeProfilePipe(ctx context.Context, producer profilePipeProducer) (*profilePipe, error) {
	// Verify configured producer material before any native resource allocation.
	m, frame, err := prepareProfilePipe(ctx, producer)
	if err != nil {
		return nil, err
	}
	api, err := nativeProfileSyncAPI()
	if err != nil {
		return nil, err
	}
	return newPreparedProfilePipe(ctx, m, frame, winProfilePipeKernel{}, api)
}

func (winProfilePipeKernel) create() (uintptr, uintptr, error) {
	var reader, writer windows.Handle
	// NULL security attributes make both original endpoints non-inheritable.
	// CreatePipe is synchronous; no FILE_FLAG_OVERLAPPED endpoint is involved.
	err := windows.CreatePipe(&reader, &writer, nil, 0)
	return uintptr(reader), uintptr(writer), err
}

func (winProfilePipeKernel) inheritReader(handle uintptr) (uintptr, error) {
	var reader windows.Handle
	// Do not use DUPLICATE_SAME_ACCESS: the sole child endpoint is read-only,
	// SYNCHRONIZE | READ_CONTROL | FILE_READ_DATA | FILE_READ_EA | FILE_READ_ATTRIBUTES.
	err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(handle), windows.CurrentProcess(), &reader, 0x120089, true, 0)
	return uintptr(reader), err
}

func (winProfilePipeKernel) close(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}
