package localcontrol

import (
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
)

func testConfig() Config {
	return Config{OperatorSID: "S-1-5-21-101-102-103-1001", ServiceSID: "S-1-5-80-11-22-33-44-55", GuestSID: "S-1-5-21-101-102-103-1002", Generation: 1, Identity: backend.Identity{LaunchID: "control-test", Epoch: "1", PolicyDigest: strings.Repeat("a", 64)}, ChannelNonce: strings.Repeat("c", 64)}
}

func TestExplicitPolicyProducesLocalLeastPrivilegePlan(t *testing.T) {
	config := testConfig()
	policy, err := NewPolicy(config)
	if err != nil {
		t.Fatal("explicit policy rejected")
	}
	plan := policy.Plan()
	if !strings.HasPrefix(plan.Name, `\\.\pipe\rescue-host-control-1-`) || strings.Contains(plan.Name, "/") {
		t.Fatal("local pipe namespace not fixed")
	}
	if plan.OpenMode != 0x40080003 || plan.PipeMode != 0x0000000e || plan.MaxInstances != 1 || plan.BufferBytes != 16384 {
		t.Fatal("native flags differ from first/local-only message pipe")
	}
	if plan.ClientAccess != 0x00100183 || plan.ClientAccess&0x4 != 0 || plan.ClientAccess&0x40000000 != 0 {
		t.Fatal("operator can create pipe instances or has generic write")
	}
	want := "D:P(D;;0x001f01ff;;;" + config.GuestSID + ")(A;;0x001f01ff;;;" + config.ServiceSID + ")(A;;0x00100183;;;" + config.OperatorSID + ")"
	if plan.SDDL != want {
		t.Fatal("explicit protected DACL mismatch")
	}
}

func TestIncompleteOrBroadPolicyNeverBuilds(t *testing.T) {
	for _, name := range []string{"empty", "operator-empty", "service-empty", "guest-empty", "system", "everyone", "shared-service", "same-guest", "same-service", "sddl-injection", "leading-zero-sid", "sid-overflow", "generation-zero", "epoch-noncanonical", "bad-launch", "bad-nonce"} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			switch name {
			case "empty":
				config = Config{}
			case "operator-empty":
				config.OperatorSID = ""
			case "service-empty":
				config.ServiceSID = ""
			case "guest-empty":
				config.GuestSID = ""
			case "system":
				config.ServiceSID = "S-1-5-18"
			case "everyone":
				config.OperatorSID = "S-1-1-0"
			case "shared-service":
				config.ServiceSID = "S-1-5-80-0"
			case "same-guest":
				config.GuestSID = config.OperatorSID
			case "same-service":
				config.ServiceSID = config.OperatorSID
			case "sddl-injection":
				config.OperatorSID += ";)(A;;GA;;;WD)"
			case "leading-zero-sid":
				config.OperatorSID = "S-1-5-21-101-102-103-01001"
			case "sid-overflow":
				config.GuestSID = "S-1-5-21-101-102-103-4294967296"
			case "generation-zero":
				config.Generation = 0
			case "epoch-noncanonical":
				config.Identity.Epoch = "01"
			case "bad-launch":
				config.Identity.LaunchID = "../operator"
			case "bad-nonce":
				config.ChannelNonce = strings.Repeat("C", 64)
			}
			if _, err := NewPolicy(config); err != ErrConfig {
				t.Fatal("unsafe/default policy accepted")
			}
		})
	}
}

func TestReturnedPlanCannotMutatePolicy(t *testing.T) {
	p, _ := NewPolicy(testConfig())
	plan := p.Plan()
	plan.Name = `\\server\pipe\arbitrary`
	plan.SDDL = "D:(A;;GA;;;WD)"
	if p.Plan().Name == plan.Name || p.Plan().SDDL == plan.SDDL {
		t.Fatal("caller changed immutable plan")
	}
}
