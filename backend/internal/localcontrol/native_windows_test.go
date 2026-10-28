//go:build windows

package localcontrol

import (
	"testing"

	"golang.org/x/sys/windows"
)

// SDK定数だけを照合する。native API、pipe、account、ACLは作成しない。
func TestWindowsPlanMatchesFixedSDK(t *testing.T) {
	policy, _ := NewPolicy(testConfig())
	plan := policy.Plan()
	if plan.OpenMode != windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE {
		t.Fatal("server open flags do not match SDK")
	}
	if plan.PipeMode != windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_REJECT_REMOTE_CLIENTS {
		t.Fatal("local-only message flags do not match SDK")
	}
	want := uint32(windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES | windows.FILE_WRITE_ATTRIBUTES | windows.SYNCHRONIZE)
	if plan.ClientAccess != want || plan.ClientAccess&(windows.FILE_APPEND_DATA|windows.GENERIC_WRITE|windows.GENERIC_READ) != 0 {
		t.Fatal("operator access includes pipe-instance/generic rights")
	}
}
