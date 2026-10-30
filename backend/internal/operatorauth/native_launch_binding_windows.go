//go:build windows

package operatorauth

import (
	"context"
	"encoding/binary"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type launchPrimaryToken struct {
	tokenProjection
	tokenID, modifiedID uint64
}

func launchPrimaryProjection(t windows.Token) (launchPrimaryToken, error) {
	kind, err := nativeTokenDWORD(t, windows.TokenType)
	if err != nil || kind != windows.TokenPrimary {
		return launchPrimaryToken{}, ErrDenied
	}
	user, _, _, _, err := nativeTokenSID(t, windows.TokenUser)
	if err != nil {
		return launchPrimaryToken{}, ErrDenied
	}
	session, err := nativeTokenDWORD(t, windows.TokenSessionId)
	if err != nil {
		return launchPrimaryToken{}, ErrDenied
	}
	restricted, err := nativeTokenDWORD(t, windows.TokenHasRestrictions)
	if err != nil {
		return launchPrimaryToken{}, ErrDenied
	}
	app, err := nativeTokenDWORD(t, nativeTokenIsAppContainer)
	if err != nil {
		return launchPrimaryToken{}, ErrDenied
	}
	_, authority, count, integrity, err := nativeTokenSID(t, windows.TokenIntegrityLevel)
	if err != nil || authority != 16 || count != 1 {
		return launchPrimaryToken{}, ErrDenied
	}
	var storage [7]uint64
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 56)
	var size uint32
	if windows.GetTokenInformation(t, windows.TokenStatistics, &data[0], uint32(len(data)), &size) != nil || size != uint32(len(data)) || binary.LittleEndian.Uint32(data[24:28]) != windows.TokenPrimary {
		return launchPrimaryToken{}, ErrDenied
	}
	return launchPrimaryToken{tokenProjection: tokenProjection{user: user, authentication: binary.LittleEndian.Uint64(data[8:16]), session: session, integrity: integrity, kind: kind, restricted: restricted != 0, appContainer: app != 0}, tokenID: binary.LittleEndian.Uint64(data[:8]), modifiedID: binary.LittleEndian.Uint64(data[48:56])}, nil
}

// Query the held token with a fixed, pinned buffer. No unbounded realloc or
// membership approximation may replace the actual SID_AND_ATTRIBUTES value.
func launchServiceGroup(t windows.Token, serviceSID string) (helperServiceGroup, error) {
	var storage [8192]uint64
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 64*1024)
	var pins runtime.Pinner
	pins.Pin(&storage[0])
	defer pins.Unpin()
	var returned uint32
	if windows.GetTokenInformation(t, windows.TokenGroups, &data[0], uint32(len(data)), &returned) != nil || returned < 4 || returned > uint32(len(data)) {
		return helperServiceGroup{}, ErrDenied
	}
	groups := (*windows.Tokengroups)(unsafe.Pointer(&data[0]))
	end := unsafe.Offsetof(groups.Groups) + uintptr(groups.GroupCount)*unsafe.Sizeof(windows.SIDAndAttributes{})
	if groups.GroupCount > 1024 || end > uintptr(returned) {
		return helperServiceGroup{}, ErrDenied
	}
	seen := make(map[string]bool, groups.GroupCount)
	var result helperServiceGroup
	for _, group := range groups.AllGroups() {
		base, address := uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(group.Sid))
		if address < base || address%4 != 0 || address-base < end || address-base > uintptr(returned)-8 {
			return helperServiceGroup{}, ErrDenied
		}
		sid := data[address-base : uintptr(returned)]
		if sid[0] != 1 || sid[1] > 15 || 8+4*int(sid[1]) > len(sid) || !group.Sid.IsValid() {
			return helperServiceGroup{}, ErrDenied
		}
		name := group.Sid.String()
		if !reportSIDValid(name) || seen[name] {
			return helperServiceGroup{}, ErrDenied
		}
		seen[name] = true
		if name == serviceSID {
			result = helperServiceGroup{name, group.Attributes}
		}
	}
	if !result.matches(serviceSID) {
		return helperServiceGroup{}, ErrDenied
	}
	return result, nil
}

// Each query uses the originally held process/image handles. It never opens a
// PID. Normal exit does not inherently invalidate creation/image/token binding.
// First inspect freezes source TokenID/ModifiedID and child TokenID/ModifiedID;
// later checks reject every drift without refreshing the baseline. Source and
// child TokenID need not equal because creation may duplicate the primary token.
// This is post-binding change detection, not issuance provenance or proof of no
// mutation before/during first binding. The missing protected supplier and native
// validation must address that interval and CreateProcessAsUser's temporary
// privilege/ModifiedID behavior. No runtime behavior has been demonstrated here.
func (nativeHelperKernel) inspect(ctx context.Context, process, image uintptr, s nativeLaunchSpec) (out helperBinding, result error) {
	if !finite(ctx) || launchCheckImage(ctx, image, s) != nil {
		return out, ErrDenied
	}
	h := windows.Handle(process)
	pid, err := windows.GetProcessId(h)
	if err != nil || pid == 0 {
		return out, ErrDenied
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return out, ErrDenied
	}
	var path [32768]uint16
	size := uint32(len(path))
	if windows.QueryFullProcessImageName(h, 0, &path[0], &size) != nil || size == 0 || size >= uint32(len(path)) || windows.UTF16ToString(path[:size]) != s.imagePath {
		return out, ErrDenied
	}
	var token windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token) != nil {
		return out, ErrDenied
	}
	defer func() {
		if token.Close() != nil {
			out = helperBinding{}
			result = ErrUnconfirmed
		}
	}()
	actual, err := launchPrimaryProjection(token)
	if err != nil {
		return out, ErrDenied
	}
	expected, err := launchPrimaryProjection(windows.Token(s.primaryToken))
	if err != nil || actual.tokenProjection != expected.tokenProjection || actual.user != s.helperUserSID || !finite(ctx) {
		return out, ErrDenied
	}
	var sourceGroup, childGroup helperServiceGroup
	if s.helperServiceSID != "" {
		childGroup, err = launchServiceGroup(token, s.helperServiceSID)
		if err != nil {
			return out, ErrDenied
		}
		sourceGroup, err = launchServiceGroup(windows.Token(s.primaryToken), s.helperServiceSID)
		if err != nil || !finite(ctx) {
			return out, ErrDenied
		}
	}
	out = helperBinding{pid: pid, creation: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), tokenID: actual.tokenID, childModifiedID: actual.modifiedID, sourceTokenID: expected.tokenID, sourceModifiedID: expected.modifiedID, user: actual.user, imagePath: s.imagePath, primary: actual.tokenProjection, sourceServiceGroup: sourceGroup, childServiceGroup: childGroup}
	if !out.valid(s) {
		return helperBinding{}, ErrDenied
	}
	return out, nil
}

type launchJobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
	PageFaultCount, TotalProcesses, ActiveProcesses, Terminated      uint32
}

func (nativeHelperKernel) waitEmpty(ctx context.Context, job uintptr) error {
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	for finite(ctx) {
		var info launchJobAccounting
		if windows.QueryInformationJobObject(windows.Handle(job), windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil) != nil {
			return ErrUnconfirmed
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ErrUnconfirmed
}
