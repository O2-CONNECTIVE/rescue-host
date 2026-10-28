//go:build !windows

package servicehost_test

import (
	"errors"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func TestNonWindowsSCMEntryHasNoFallback(t *testing.T) {
	if err := servicehost.RunSCM(); !errors.Is(err, servicehost.ErrUnsupportedPlatform) {
		t.Fatalf("non-Windows SCM entry accepted: %v", err)
	}
}
