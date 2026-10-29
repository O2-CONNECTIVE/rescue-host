package guestaccount

import (
	"context"
	"strings"
)

func (l *Lifecycle) failure(err error) error {
	if _, known := err.(knownFailure); known {
		l.state = Cleaning
		return ErrCleanup
	}
	l.unknown = true
	l.state = Unknown
	return ErrUnknown
}

// 明白な管理権限の誤設定を拒否する最低限のguardであり、隔離の網羅証明でない。
// https://learn.microsoft.com/en-us/windows/win32/secauthz/privilege-constants
func administrativePolicy(p limitedPolicy) bool {
	for _, group := range p.groups {
		if group == "S-1-5-32-544" {
			return true
		}
	}
	for _, privilege := range p.privileges {
		for _, denied := range []string{"SeDebugPrivilege", "SeTcbPrivilege", "SeCreateTokenPrivilege", "SeAssignPrimaryTokenPrivilege", "SeImpersonatePrivilege", "SeLoadDriverPrivilege", "SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege", "SeDelegateSessionUserImpersonatePrivilege", "SeTrustedCredManAccessPrivilege", "SeShutdownPrivilege", "SeRemoteShutdownPrivilege", "SeSystemtimePrivilege", "SeManageVolumePrivilege", "SeSecurityPrivilege", "SeRelabelPrivilege", "SeSystemEnvironmentPrivilege"} {
			if strings.EqualFold(privilege, denied) {
				return true
			}
		}
	}
	return false
}
func (l *Lifecycle) save(ctx context.Context, next stage, reserve bool) error {
	if !finite(ctx) {
		return ErrCleanup
	}
	r := record{binding: l.record.binding, stage: next}
	var receipt journalReceipt
	var err error
	if reserve {
		receipt, err = l.sources.journal.reserve(ctx, r)
	} else {
		receipt, err = l.sources.journal.save(ctx, r)
	}
	if err != nil || receipt.seal != l.sources.journalSeal || receipt.record != r {
		if err == nil {
			err = ErrUnknown
		}
		return l.failure(err)
	}
	l.record = r
	if !finite(ctx) {
		l.state = Cleaning
		return ErrCleanup
	}
	return nil
}
func (l *Lifecycle) accountReceipt(r accountReceipt, k accountKind) bool {
	return r.seal == l.sources.accountSeal && r.kind == k && r.binding == l.record.binding && l.plan.validBinding(r.binding)
}
func (l *Lifecycle) sessionReceipt(r sessionReceipt, k sessionKind) bool {
	return r.seal == l.sources.sessionSeal && r.kind == k && r.binding == l.record.binding
}
func (l *Lifecycle) snapshot(r accountSnapshot, state presence) bool {
	return r.seal == l.sources.accountSeal && r.binding == l.record.binding && r.presence == state && r.complete && l.plan.validBinding(r.binding)
}
func (l *Lifecycle) limited(r accountSnapshot) bool {
	return l.snapshot(r, exact) && r.groups != nil && r.privileges != nil && r.logonRights != nil && r.disabled && !r.administrator && !r.protectedWrites && subset(r.groups, l.plan.policy.groups) && subset(r.privileges, l.plan.policy.privileges) && subset(r.logonRights, l.plan.policy.logonRights)
}
func subset(actual, allowed []string) bool {
	if !labels(actual, true) {
		return false
	}
	for _, value := range actual {
		found := false
		for _, permit := range allowed {
			if value == permit {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
