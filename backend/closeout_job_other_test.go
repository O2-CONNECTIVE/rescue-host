//go:build !windows

package backend

import (
	"errors"
	"os"
)

func inheritFixtureJob() (bool, error) {
	if os.Getenv("RESCUE_SESSION_JOB_HANDLE") != "" {
		return false, errors.New("Windows専用fixture JobをこのOSでは使用しません")
	}
	return false, nil
}
