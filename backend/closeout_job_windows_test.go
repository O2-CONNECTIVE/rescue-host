package backend

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// 継承元は今回の親が新規作成したunnamed Jobだけ。PIDを使って別processを開かない。
func inheritFixtureJob() (bool, error) {
	text := os.Getenv("RESCUE_SESSION_JOB_HANDLE")
	if text == "" {
		return false, nil
	}
	value, e := strconv.ParseUint(text, 10, 64)
	if e != nil || value == 0 {
		return false, fmt.Errorf("fixture Job handle形式不正")
	}
	h := windows.Handle(value)
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	e = windows.QueryInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	if e != nil {
		windows.CloseHandle(h)
		return false, fmt.Errorf("QueryInformationJobObject(今回Job): %w", e)
	}
	if info.BasicLimitInformation.LimitFlags != windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE {
		windows.CloseHandle(h)
		return false, fmt.Errorf("fixture Jobの停止方針不一致")
	}
	e = windows.AssignProcessToJobObject(h, windows.CurrentProcess())
	closeErr := windows.CloseHandle(h)
	if e != nil {
		return false, fmt.Errorf("AssignProcessToJobObject(子自身): %w", e)
	}
	if closeErr != nil {
		return false, fmt.Errorf("CloseHandle(子の継承Job): %w", closeErr)
	}
	return true, nil
}

// 保護主体/ACL/既存Jobは変更しない。新規Jobと自己子だけのOS終了経路を観測する。
func TestCloseoutN14OwnedJobClosesSession(t *testing.T) {
	attributes := windows.SecurityAttributes{InheritHandle: 1}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	job, e := windows.CreateJobObject(&attributes, nil)
	if e != nil {
		t.Fatalf("CreateJobObject(新規unnamed)拒否/失敗: %v", e)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if e := windows.CloseHandle(job); e != nil {
				t.Errorf("今回JobのClose未確認: %v", e)
			}
		}
	})
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, e = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); e != nil {
		t.Fatalf("SetInformationJobObject(今回Job)拒否/失敗: %v", e)
	}
	budget := &fixtureBudget{now: time.Now, deadline: time.Now().Add(time.Minute), children: map[*ownedSessionChild]bool{}, prepare: func(cmd *exec.Cmd) error {
		cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(job)}}
		cmd.Env = append(cmd.Env, "RESCUE_SESSION_JOB_HANDLE="+strconv.FormatUint(uint64(job), 10))
		return nil
	}}
	r, f, cfg, cl := childFixture(t, "fault", budget)
	if !r.ready.JobAttached {
		t.Fatal("自己子のJob割当/継承handle閉鎖確認なし")
	}
	v := r.command(t, sessionChildCommand{cfg.Identity, "revoke", 0})
	if !v.Failed || v.Snapshot.State != "revoking" || v.Snapshot.Owned != 2 {
		t.Fatal("adapter/独立停止故障を終了扱い")
	}
	// control pipeがまだ生存し、3秒TTLより十分早いことをClose直前に確認する。
	v = r.command(t, sessionChildCommand{cfg.Identity, "renew", 2})
	if !v.Failed || v.PID != r.cmd.Process.Pid || v.Snapshot.State != "revoking" {
		t.Fatal("Job停止直前の自己子状態不明")
	}
	if time.Since(r.startedMono) >= time.Second {
		t.Fatal("TTLとの因果を区別する準備時間上限を超過")
	}
	trigger := time.Now()
	if e = windows.CloseHandle(job); e != nil {
		t.Fatalf("CloseHandle(親の最終Job handle)失敗: %v", e)
	}
	closed = true
	r.wait(t, false, time.Second)
	if r.killRequested || r.cmd.ProcessState.ExitCode() == 124 {
		t.Fatal("fallback Kill/TTLをJob終了と誤認")
	}
	assertChildTargetEOF(t, f, trigger.Add(time.Second))
	done := make(chan error, 1)
	go func() { done <- cl.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Until(trigger.Add(time.Second))):
		t.Fatal("Job終了後のSSH終端が1秒超過")
	}
	if time.Since(trigger) >= time.Second {
		t.Fatal("Job終了から両端終端の1秒条件を超過")
	}
	childEvidence = append(childEvidence, map[string]any{"pid": r.cmd.Process.Pid, "job_close_succeeded": true, "child_assigned_self": true, "inherited_job_handle_closed_before_ready": true, "fallback_kill": false, "independent_ttl_exit": false, "process_exit_code": r.cmd.ProcessState.ExitCode(), "target_and_ssh_end_ms": time.Since(trigger).Milliseconds(), "protected_supervisor": false})
	t.Logf("新規unnamed Job最終handle Close→自己子Wait/target/SSH終端=%dms、fallback Killなし・TTL未到達・保護supervisor=false", time.Since(trigger).Milliseconds())
}
