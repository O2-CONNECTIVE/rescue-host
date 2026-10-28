package childjob

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUnconfiguredStartDoesNotLaunch(t *testing.T) {
	child, err := Start(context.Background(), Spec{})
	if child != nil || !errors.Is(err, ErrConfig) {
		t.Fatalf("未設定の起動を受理しました: child=%v error=%v", child, err)
	}
}

func approvedSpec() Spec {
	return Spec{Artifact: Artifact{Path: `C:\protected\rescue-backend.exe`, SHA256: [32]byte{1}}, Handles: Handles{10, 11, 12}, PrimaryToken: 20, CleanupTimeout: time.Second}
}

type memoryKernel struct {
	calls     []string
	failure   string
	failures  map[string]bool
	emptyFail bool
	exitCode  uint32
	onCall    func(string)
}

func (m *memoryKernel) record(name string) error {
	m.calls = append(m.calls, name)
	if m.onCall != nil {
		m.onCall(name)
	}
	if m.failure == name || m.failures[name] {
		return errors.New("synthetic OS failure")
	}
	return nil
}

func (m *memoryKernel) verifyArtifact(context.Context, Artifact) (uintptr, error) {
	return 1, m.record("verify")
}
func (m *memoryKernel) newJob() (uintptr, error) {
	return 2, m.record("job")
}
func (m *memoryKernel) createSuspended(Spec) (uintptr, uintptr, error) {
	return 3, 4, m.record("create-suspended")
}
func (m *memoryKernel) assign(uintptr, uintptr) error {
	return m.record("assign")
}
func (m *memoryKernel) resume(uintptr) error {
	return m.record("resume")
}
func (m *memoryKernel) terminateJob(uintptr) error {
	return m.record("terminate-job")
}
func (m *memoryKernel) terminateProcess(uintptr) error {
	return m.record("terminate-process")
}
func (m *memoryKernel) waitProcess(ctx context.Context, _ uintptr) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return m.exitCode, m.record("wait-process")
}
func (m *memoryKernel) waitEmpty(context.Context, uintptr) error {
	if err := m.record("wait-empty"); err != nil {
		return err
	}
	if m.emptyFail {
		return errors.New("synthetic descendants remain")
	}
	return nil
}
func (m *memoryKernel) close(handle uintptr) error {
	return m.record(map[uintptr]string{1: "close-image", 2: "close-job", 3: "close-process", 4: "close-thread"}[handle])
}

func TestStartAssignsBeforeResumeAndCloseConfirmsOwnedJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{}
	child, err := start(ctx, approvedSpec(), api)
	if child == nil || err != nil {
		t.Fatalf("承認済起動を確認できません: %v", err)
	}
	want := []string{"verify", "job", "create-suspended", "assign", "resume", "close-thread", "close-image"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("Job割当前に実行しました: %v", api.calls)
	}
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
	want = append(want, "terminate-job", "wait-process", "wait-empty", "close-process", "close-job")
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("所有回収を確認せずhandleを破棄しました: %v", api.calls)
	}
}

func TestNativePlanHasFixedArgumentsEnvironmentAndHandleWhitelist(t *testing.T) {
	spec := approvedSpec()
	plan, err := makePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if plan.application != spec.Artifact.Path || plan.commandLine != `"C:\protected\rescue-backend.exe" --supervised-internal` || plan.directory != `C:\protected` {
		t.Fatalf("固定image以外の検索や引数を許しました: %+v", plan)
	}
	if plan.flags != 0x08080404 || plan.startFlags != 0x100 || plan.environment != [2]uint16{} || plan.handles != [3]uintptr{10, 11, 12} {
		t.Fatalf("suspended起動または限定継承が変わりました: %+v", plan)
	}
}

func TestDriveRootImageKeepsAnAbsoluteWorkingDirectory(t *testing.T) {
	spec := approvedSpec()
	spec.Artifact.Path = `C:\rescue-backend.exe`
	plan, err := makePlan(spec)
	if err != nil || plan.directory != `C:\` {
		t.Fatalf("driveのambient cwdを採用しました: %q %v", plan.directory, err)
	}
}

func TestPseudoHandlesAreRejectedBeforeTouchingKernel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, target := range []string{"stdin", "stdout", "stderr", "token"} {
		t.Run(target, func(t *testing.T) {
			spec := approvedSpec()
			switch target {
			case "stdin":
				spec.Handles.Stdin = ^uintptr(1)
			case "stdout":
				spec.Handles.Stdout = ^uintptr(2)
			case "stderr":
				spec.Handles.Stderr = ^uintptr(3)
			case "token":
				spec.PrimaryToken = ^uintptr(4)
			}
			api := &memoryKernel{}
			child, err := start(ctx, spec, api)
			if child != nil || !errors.Is(err, ErrConfig) || len(api.calls) != 0 {
				t.Fatal("pseudo handleが起動処理へ入りました")
			}
		})
	}
}

func TestLeaderExitDoesNotSuppressTerminationOfRemainingJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	api.emptyFail = true
	if err := child.Wait(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("leader退出をJob終了へ読み替えました: %v", err)
	}
	for _, call := range api.calls {
		if call == "close-job" || call == "close-process" {
			t.Fatalf("未確認の所有参照を捨てました: %v", api.calls)
		}
	}
	api.emptyFail = false
	api.calls = nil
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) == 0 || api.calls[0] != "terminate-job" {
		t.Fatalf("leader退出後のJob停止を省略しました: %v", api.calls)
	}
}

// 以下は既存のOS seamに対する失敗回収の回帰検査。実processは作らない。
func TestStartupFailureNeverResumesAnUnassignedProcess(t *testing.T) {
	for _, failure := range []string{"verify", "job", "create-suspended", "assign", "resume"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			api := &memoryKernel{failure: failure}
			child, err := start(ctx, approvedSpec(), api)
			if child != nil || !errors.Is(err, ErrStart) || strings.Contains(err.Error(), "synthetic") {
				t.Fatalf("起動失敗を成功や未選別診断へ変えました: %v", err)
			}
			for _, call := range api.calls {
				if call == "resume" && failure != "resume" {
					t.Fatalf("失敗後に子を実行しました: %v", api.calls)
				}
			}
		})
	}
}

func TestUnconfirmedStartupReturnsOwnedChildForLaterCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{failures: map[string]bool{"assign": true, "wait-process": true}}
	child, err := start(ctx, approvedSpec(), api)
	if child == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("部分起動の所有参照を失いました: %v", err)
	}
	for _, call := range api.calls {
		if call == "resume" || call == "close-process" || call == "close-job" {
			t.Fatalf("未確認の子を実行/破棄しました: %v", api.calls)
		}
	}
	api.failures = nil
	api.calls = nil
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"terminate-job", "terminate-process", "wait-process", "wait-empty", "close-thread", "close-image", "close-process", "close-job"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("部分起動を再確認できません: %v", api.calls)
	}
}

func TestHandleCloseFailureRemainsOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{failure: "close-thread"}
	child, err := start(ctx, approvedSpec(), api)
	if child == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("閉鎖失敗したthread handleを捨てました")
	}
	api.failure = ""
	api.calls = nil
	if err := child.Close(ctx); err != nil || !reflect.DeepEqual(api.calls, []string{"close-thread"}) {
		t.Fatalf("残存handleの回収を再確認できません: %v %v", err, api.calls)
	}
}

func TestWaitReportsAbnormalExitWithoutInventingGoCloseEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{exitCode: 2}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(ctx); !errors.Is(err, ErrExit) {
		t.Fatalf("非0退出を通常成功へ変えました: %v", err)
	}
	if err := child.Close(ctx); err != nil {
		t.Fatalf("確認済み所有物が再び未確認になりました: %v", err)
	}
}

func TestConcurrentWaitAndCloseReleaseEachOwnedHandleOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			var err error
			if index%2 == 0 {
				err = child.Wait(ctx)
			} else {
				err = child.Close(ctx)
			}
			if err != nil {
				t.Errorf("並行終了の確認失敗: %v", err)
			}
		}(index)
	}
	workers.Wait()
	for _, name := range []string{"close-image", "close-thread", "close-process", "close-job"} {
		count := 0
		for _, call := range api.calls {
			if call == name {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("所有handleの閉鎖回数が不正: %s=%d", name, count)
		}
	}
}

func TestArtifactPlanRejectsSearchUNCDeviceAndAmbiguousPaths(t *testing.T) {
	for _, path := range []string{"rescue-backend.exe", `C:rescue-backend.exe`, `\\server\share\rescue-backend.exe`, `\\?\C:\protected\rescue-backend.exe`, `C:\protected\..\rescue-backend.exe`, `C:\protected\\rescue-backend.exe`, `C:\protected\rescue-backend.exe:stream`, `C:\protected.\rescue-backend.exe`, `C:/protected/rescue-backend.exe`, "C:\\protected\\bad\n.exe"} {
		spec := approvedSpec()
		spec.Artifact.Path = path
		if _, err := makePlan(spec); !errors.Is(err, ErrConfig) {
			t.Fatalf("曖昧な起動pathを受理しました: %q", path)
		}
	}
}

func TestStartRejectsUnboundedOrCanceledContextBeforeKernel(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), canceled} {
		api := &memoryKernel{}
		child, err := start(ctx, approvedSpec(), api)
		if child != nil || !errors.Is(err, ErrConfig) || len(api.calls) != 0 {
			t.Fatal("有限の開始予算なしでOSを呼びました")
		}
	}
}

func TestStartupCancellationUsesIndependentCleanupBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{onCall: func(name string) {
		if name == "create-suspended" {
			cancel()
		}
	}}
	child, err := start(ctx, approvedSpec(), api)
	if child != nil || !errors.Is(err, ErrStart) {
		t.Fatalf("取消後の部分起動を回収できません: %v", err)
	}
	want := []string{"verify", "job", "create-suspended", "terminate-job", "terminate-process", "wait-process", "wait-empty", "close-thread", "close-image", "close-process", "close-job"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("取消した子を実行、または回収を省略しました: %v", api.calls)
	}
}
