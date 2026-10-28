package operatorauth

import (
	"context"
	"strings"
	"testing"
	"time"
)

type reportAcquireKernel struct {
	reportFake
	stage      string
	value      uintptr
	closeFails bool
}

func (k *reportAcquireKernel) create(n, d string) (uintptr, error) {
	if k.stage == "create" {
		return k.value, ErrDenied
	}
	return k.reportFake.create(n, d)
}
func (k *reportAcquireKernel) open(n string) (uintptr, error) {
	if k.stage == "open" {
		return k.value, ErrDenied
	}
	return k.reportFake.open(n)
}
func (k *reportAcquireKernel) duplicate(h uintptr) (uintptr, error) {
	if k.stage == "duplicate" {
		return k.value, ErrDenied
	}
	if h != 12 {
		return 0, ErrDenied
	}
	return k.reportFake.duplicate(h)
}
func (k *reportAcquireKernel) close(h uintptr) error {
	if k.closeFails {
		return ErrUnconfirmed
	}
	return k.reportFake.close(h)
}
func TestReportAcquisitionDoesNotOwnInvalidSentinels(t *testing.T) {
	for _, stage := range []string{"create", "open", "duplicate"} {
		t.Run(stage, func(t *testing.T) {
			k := &reportAcquireKernel{stage: stage, value: ^uintptr(0)}
			p, err := newReportPair(reportContext(t), reportSetup(), k)
			if err == nil || p != nil {
				t.Fatal("invalid acquisition accepted or retained")
			}
			for _, h := range k.closed {
				if h == ^uintptr(0) {
					t.Fatal("invalid sentinel closed as an owned handle")
				}
			}
		})
	}
}
func TestReportPartialAcquisitionKeepsFailedCleanupOwner(t *testing.T) {
	for _, stage := range []string{"create", "open", "duplicate"} {
		t.Run(stage, func(t *testing.T) {
			k := &reportAcquireKernel{stage: stage, value: 44, closeFails: true}
			p, err := newReportPair(reportContext(t), reportSetup(), k)
			if err == nil || p == nil || p.childWriter() != 0 {
				t.Fatal("partial handle owner lost or usable")
			}
			k.closeFails = false
			if err = p.close(reportContext(t)); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, h := range k.closed {
				if h == 44 {
					found = true
				}
			}
			if !found {
				t.Fatal("partial handle never reclaimed")
			}
		})
	}
}
func TestReportSetupRequiresExplicitDistinctSIDAndOpaqueName(t *testing.T) {
	s := reportSetup()
	k := &reportAcquireKernel{}
	p, err := newReportPair(reportContext(t), s, k)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close(reportContext(t))
	prefix := `\\.\pipe\rescue-auth-report-`
	if !strings.HasPrefix(k.name, prefix) || len(strings.TrimPrefix(k.name, prefix)) != 64 {
		t.Fatal("report name lacks random-only identifier")
	}
	if k.descriptor != "O:S-1-5-21-1D:P(D;;GA;;;S-1-5-21-4)(A;;GA;;;S-1-5-21-1)(A;;GA;;;S-1-5-80-2)(A;;0x120102;;;S-1-5-21-3)" {
		t.Fatal("unexpected protected ACL")
	}
	for _, guest := range []string{"", s.ownerSID, s.serviceSID, s.helperSID, "S-1-05-4", "S-1-5-4294967296"} {
		s.guestSID = guest
		if p, err := newReportPair(reportContext(t), s, k); p != nil || err == nil {
			t.Fatalf("bad guest accepted: %q", guest)
		}
	}
}

type reportPrincipalKernel struct {
	reportFake
	user   string
	groups map[string]uint32
	err    error
}

func (k *reportPrincipalKernel) primary() (string, map[string]uint32, error) {
	return k.user, k.groups, k.err
}
func TestReportFactoryVerifiesCurrentPrimaryOwnerAndEnabledService(t *testing.T) {
	for _, tt := range []struct {
		user  string
		attrs uint32
		err   error
	}{
		{"S-1-5-21-99", 4, nil}, {"S-1-5-21-1", 0, nil}, {"S-1-5-21-1", 0x14, nil}, {"S-1-5-21-1", 4, ErrDenied},
	} {
		k := &reportPrincipalKernel{user: tt.user, groups: map[string]uint32{"S-1-5-80-2": tt.attrs}, err: tt.err}
		p, err := newReportPair(reportContext(t), reportSetup(), k)
		if err == nil || p != nil || k.name != "" {
			t.Fatal("unverified principal created report pipe")
		}
	}
}

type reportExpiredSetupKernel struct {
	reportFake
	cancel func()
}

func (k *reportExpiredSetupKernel) primary() (string, map[string]uint32, error) {
	k.cancel()
	return k.reportFake.primary()
}
func TestReportSetupDoesNotCreateAfterIdentityCheckExpires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k := &reportExpiredSetupKernel{cancel: cancel}
	p, err := newReportPair(ctx, reportSetup(), k)
	if p != nil || err == nil || k.name != "" {
		t.Fatal("expired setup created a report pipe")
	}
}
