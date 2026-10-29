//go:build !windows

package operatorauth

func nativeProfileSyncAPI() (profileSyncAPI, error) { return nil, ErrUnconfigured }
