package operatorauth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const isolatedHelperServiceSID = "S-1-5-80-1-2-3-4-5"

func TestBatchLocalServiceEnabledOwnerGroupKeepsDedicatedReportAccess(t *testing.T) {
	s, owner, ops, report, kernel := localServiceBatchFixture(t)
	kernel.binding.sourceServiceGroup.attributes = 0xf
	kernel.binding.childServiceGroup.attributes = 0xf
	batch, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = batch.Start(requestContext(t)); err != nil {
		t.Fatal("enabled service group with OWNER attribute was denied", err)
	}
	if !strings.Contains(report.descriptor, "(A;;0x120102;;;S-1-5-80-1-2-3-4-5)") || strings.Contains(report.descriptor, ";;;S-1-5-19)") {
		t.Fatal("group OWNER broadened report access", report.descriptor)
	}
	if err = batch.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestBatchLocalServiceRejectsMissingSourceServiceGroup(t *testing.T) {
	s, owner, ops, _, kernel := localServiceBatchFixture(t)
	kernel.binding.sourceServiceGroup = helperServiceGroup{}
	batch, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = batch.Start(requestContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("source without service group started helper", err)
	}
	if !owner.issuer.Snapshot().Closed || strings.Contains(strings.Join(kernel.events, ","), "resume") {
		t.Fatal("rejected helper resumed or retained admission", kernel.events)
	}
	if err = batch.Close(requestContext(t)); err != nil {
		t.Fatal("same owner could not reclaim failed helper", err)
	}
}

func localServiceBatchFixture(t *testing.T) (nativeBatchSpec, batchOwner, nativeBatchOps, *reportFake, *memoryLaunchKernel) {
	t.Helper()
	s, owner, ops, _, _, _ := batchFixture(t)
	s.launch.helperUserSID = "S-1-5-19"
	s.launch.helperServiceSID = isolatedHelperServiceSID
	s.report.helperSID = isolatedHelperServiceSID
	report := &reportFake{}
	ops.newReport = func(ctx context.Context, cfg reportConfig) (batchReport, error) {
		return newReportPair(ctx, cfg, report)
	}
	_, cancel, _, kernel := launchFixture()
	cancel()
	kernel.binding.user = s.launch.helperUserSID
	kernel.binding.sourceServiceGroup = helperServiceGroup{isolatedHelperServiceSID, 7}
	kernel.binding.childServiceGroup = kernel.binding.sourceServiceGroup
	ops.start = func(ctx context.Context, spec nativeLaunchSpec) (batchChild, error) {
		child, err := startHelper(ctx, spec, kernel)
		if child == nil {
			return nil, err
		}
		return child, err
	}
	return s, owner, ops, report, kernel
}

func TestBatchLocalServiceUsesDedicatedReportPrincipal(t *testing.T) {
	s, owner, ops, report, kernel := localServiceBatchFixture(t)
	s.sidPolicy = SIDPolicy{OperatorSID: s.report.ownerSID, GuestSID: s.report.guestSID}
	var err error
	s.producer, err = newSIDProfileProvider(s.launch.launchID, s.sidPolicy, s.launch.cleanup)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal("dedicated service identity could not be prepared", err)
	}
	if !batch.MatchesOwner(owner.owner, owner.issuer) {
		t.Fatal("batch replaced service owner")
	}
	if err = batch.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if report.descriptor != "O:S-1-5-21-1D:P(D;;GA;;;S-1-5-21-4)(A;;GA;;;S-1-5-21-1)(A;;GA;;;S-1-5-80-2)(A;;0x120102;;;S-1-5-80-1-2-3-4-5)" || !strings.Contains(strings.Join(kernel.events, ","), "resume") {
		t.Fatal("dedicated helper did not start with isolated report ACL", report.descriptor, kernel.events)
	}
	if err = batch.Close(requestContext(t)); err != nil {
		t.Fatal("same owner could not reclaim helper", err)
	}
}

func TestBatchRejectsSharedLocalServiceReportPrincipal(t *testing.T) {
	s, owner, ops, _, _, _ := batchFixture(t)
	s.launch.helperUserSID = "S-1-5-19"
	s.report.helperSID = "S-1-5-19"
	report := &reportFake{}
	ops.newReport = func(ctx context.Context, cfg reportConfig) (batchReport, error) {
		return newReportPair(ctx, cfg, report)
	}
	_, cancel, _, kernel := launchFixture()
	cancel()
	kernel.binding.user = s.launch.helperUserSID
	ops.start = func(ctx context.Context, spec nativeLaunchSpec) (batchChild, error) {
		child, err := startHelper(ctx, spec, kernel)
		if child == nil {
			return nil, err
		}
		return child, err
	}
	batch, err := prepareBatch(s, owner, ops)
	if err == nil {
		err = batch.Start(requestContext(t))
		defer batch.Close(requestContext(t))
	}
	if err == nil || report.descriptor != "" || len(kernel.events) != 0 {
		t.Fatalf("shared LocalService obtained helper report access: err=%v descriptor=%q launch=%v", err, report.descriptor, kernel.events)
	}
}

// Regressions after the three observed red/green slices above. The existing OS
// seam supplies token observations; these do not prove native token provenance.
func TestBatchLocalServiceRejectsUnsafeServiceIdentityBeforeAcquisition(t *testing.T) {
	for _, test := range []struct {
		name, user, service, report string
	}{
		{"missing", "S-1-5-19", "", isolatedHelperServiceSID},
		{"all services", "S-1-5-19", "S-1-5-80-0", "S-1-5-80-0"},
		{"short SID", "S-1-5-19", "S-1-5-80-1", "S-1-5-80-1"},
		{"wrong authority", "S-1-5-19", "S-1-6-80-1-2-3-4-5", "S-1-6-80-1-2-3-4-5"},
		{"noncanonical", "S-1-5-19", "S-1-5-80-01-2-3-4-5", "S-1-5-80-01-2-3-4-5"},
		{"overflow", "S-1-5-19", "S-1-5-80-4294967296-2-3-4-5", "S-1-5-80-4294967296-2-3-4-5"},
		{"different report", "S-1-5-19", isolatedHelperServiceSID, "S-1-5-80-9-8-7-6-5"},
		{"shared report", "S-1-5-19", isolatedHelperServiceSID, "S-1-5-19"},
		{"wrong user", "S-1-5-21-3", isolatedHelperServiceSID, isolatedHelperServiceSID},
		{"LocalSystem fallback", "S-1-5-18", "", "S-1-5-18"},
		{"NetworkService fallback", "S-1-5-20", "", "S-1-5-20"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, owner, ops, report, kernel := localServiceBatchFixture(t)
			s.launch.helperUserSID, s.launch.helperServiceSID, s.report.helperSID = test.user, test.service, test.report
			batch, err := prepareBatch(s, owner, ops)
			if batch != nil || !errors.Is(err, ErrUnconfigured) || report.descriptor != "" || len(kernel.events) != 0 {
				t.Fatal("unsafe identity acquired helper resources", err, report.descriptor, kernel.events)
			}
		})
	}
}

func TestBatchLocalServiceRejectsUnusableSourceOrChildGroup(t *testing.T) {
	for _, side := range []string{"source", "child"} {
		for _, test := range []struct {
			name  string
			group helperServiceGroup
		}{
			{"missing", helperServiceGroup{}},
			{"different", helperServiceGroup{"S-1-5-80-9-8-7-6-5", 7}},
			{"disabled", helperServiceGroup{isolatedHelperServiceSID, 3}},
			{"deny only", helperServiceGroup{isolatedHelperServiceSID, 0x17}},
			{"unknown attributes", helperServiceGroup{isolatedHelperServiceSID, 0x87}},
			{"different enabled attributes", helperServiceGroup{isolatedHelperServiceSID, 5}},
		} {
			t.Run(side+"/"+test.name, func(t *testing.T) {
				s, owner, ops, _, kernel := localServiceBatchFixture(t)
				if side == "source" {
					kernel.binding.sourceServiceGroup = test.group
				} else {
					kernel.binding.childServiceGroup = test.group
				}
				batch, err := prepareBatch(s, owner, ops)
				if err != nil {
					t.Fatal(err)
				}
				if err = batch.Start(requestContext(t)); !errors.Is(err, ErrDenied) {
					t.Fatal("unusable group started helper", err)
				}
				if !owner.issuer.Snapshot().Closed || strings.Contains(strings.Join(kernel.events, ","), "resume") {
					t.Fatal("rejected helper resumed or kept admission", kernel.events)
				}
				if err = batch.Close(requestContext(t)); err != nil {
					t.Fatal("same owner could not reclaim helper", err)
				}
			})
		}
	}
}

func TestBatchLocalServiceGroupDriftDeniesResume(t *testing.T) {
	for _, side := range []string{"source", "child", "both"} {
		t.Run(side, func(t *testing.T) {
			s, owner, ops, _, kernel := localServiceBatchFixture(t)
			kernel.after = func(step string) {
				if step == "assign" {
					if side != "child" {
						kernel.binding.sourceServiceGroup.attributes = 5
					}
					if side != "source" {
						kernel.binding.childServiceGroup.attributes = 5
					}
				}
			}
			batch, err := prepareBatch(s, owner, ops)
			if err != nil {
				t.Fatal(err)
			}
			if err = batch.Start(requestContext(t)); !errors.Is(err, ErrDenied) || strings.Contains(strings.Join(kernel.events, ","), "resume") {
				t.Fatal("group drift refreshed launch baseline or resumed", err, kernel.events)
			}
			if err = batch.Close(requestContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchLocalServiceGroupFailureRetainsOwnerUntilExit(t *testing.T) {
	s, owner, ops, _, kernel := localServiceBatchFixture(t)
	kernel.binding.childServiceGroup = helperServiceGroup{}
	kernel.fail = "wait"
	batch, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = batch.Start(requestContext(t)); !errors.Is(err, ErrUnconfirmed) || !owner.issuer.Snapshot().Closed {
		t.Fatal("unconfirmed helper exit lost revocation or ownership", err)
	}
	if strings.Contains(strings.Join(kernel.events, ","), "close") {
		t.Fatal("closed helper references before exit", kernel.events)
	}
	kernel.fail, kernel.events = "", nil
	if err = batch.Close(requestContext(t)); err != nil {
		t.Fatal("original Batch failed to reclaim retained helper", err)
	}
	if strings.Count(strings.Join(kernel.events, ","), "close") != 4 {
		t.Fatal("same owner did not reclaim all original helper references", kernel.events)
	}
}

func TestBatchLegacyHelperUserKeepsReportPrincipal(t *testing.T) {
	s, owner, ops, report, kernel := localServiceBatchFixture(t)
	s.launch.helperUserSID, s.launch.helperServiceSID, s.report.helperSID = "S-1-5-21-3", "", "S-1-5-21-3"
	kernel.binding.user = s.launch.helperUserSID
	kernel.binding.sourceServiceGroup, kernel.binding.childServiceGroup = helperServiceGroup{}, helperServiceGroup{}
	batch, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = batch.Start(requestContext(t)); err != nil {
		t.Fatal("legacy dedicated user no longer launches", err)
	}
	if report.descriptor != "O:S-1-5-21-1D:P(D;;GA;;;S-1-5-21-4)(A;;GA;;;S-1-5-21-1)(A;;GA;;;S-1-5-80-2)(A;;0x120102;;;S-1-5-21-3)" {
		t.Fatal("legacy report principal changed", report.descriptor)
	}
	if err = batch.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}
