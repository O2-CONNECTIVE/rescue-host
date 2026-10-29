package operatorauth

import (
	"errors"
	"testing"
)

func TestHelperLaunchRequiresDistinctProfileReader(t *testing.T) {
	for _, use := range []string{"missing", "operator", "report", "token"} {
		t.Run(use, func(t *testing.T) {
			ctx, cancel, spec, k := launchFixture()
			defer cancel()
			switch use {
			case "missing":
				spec.profileRead = 0
			case "operator":
				spec.profileRead = spec.operatorRead
			case "report":
				spec.profileRead = spec.reportWrite
			case "token":
				spec.profileRead = spec.primaryToken
			}
			if child, err := startHelper(ctx, spec, k); child != nil || !errors.Is(err, ErrDenied) || len(k.events) != 0 {
				t.Fatal("invalid profile reader accepted", child, err, k.events)
			}
		})
	}
}
