package childjob

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFixedCommandFreezesTrustedArgumentsAndEscapesWindowsCommandLine(t *testing.T) {
	args := []string{"-NoProfile", "", `a b`, `a"b`, `C:\space dir\`}
	dependencies := []Artifact{{Path: `C:\protected\module.psd1`, SHA256: [32]byte{2}}}
	command, err := NewFixedCommand(args, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "changed"
	dependencies[0].Path = `C:\elsewhere\changed.dll`
	spec := approvedSpec()
	spec.Command = command
	plan, err := makePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\protected\rescue-backend.exe" -NoProfile "" "a b" a\"b "C:\space dir\\"`
	if plan.commandLine != want {
		t.Fatalf("固定argvを変更、またはWindows引用を失いました: %q", plan.commandLine)
	}
}

type fixedKernel struct {
	*memoryKernel
	verified []Artifact
}

func (k *fixedKernel) verifyArtifact(_ context.Context, artifact Artifact) (uintptr, error) {
	k.verified = append(k.verified, artifact)
	if len(k.verified) == 1 {
		return 1, k.record("verify")
	}
	handle := uintptr(len(k.verified) + 3)
	return handle, k.record(fmt.Sprintf("verify-dependency-%d", handle))
}

func (k *fixedKernel) close(handle uintptr) error {
	if handle >= 5 {
		return k.record(fmt.Sprintf("close-dependency-%d", handle))
	}
	return k.memoryKernel.close(handle)
}

func fixedSpec(t *testing.T) Spec {
	t.Helper()
	spec := approvedSpec()
	var err error
	spec.Command, err = NewFixedCommand([]string{"-NoProfile", "fixed"}, []Artifact{
		{Path: `C:\protected\module.psd1`, SHA256: [32]byte{2}},
		{Path: `C:\protected\module.dll`, SHA256: [32]byte{3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestFixedCommandPinsEveryDependencyUntilProcessExitAndJobEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spec := fixedSpec(t)
	original := append([]Artifact(nil), spec.Command.dependencies...)
	api := &fixedKernel{memoryKernel: &memoryKernel{onCall: func(call string) {
		if call == "verify" {
			// Start後のcaller側object変更も準備中の依存一覧へ伝播させません。
			*spec.Command = FixedCommand{}
		}
	}}}
	child, err := start(ctx, spec, api)
	if err != nil || child == nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.verified, append([]Artifact{spec.Artifact}, original...)) {
		t.Fatalf("固定した依存fileを全部検証しません: %v", api.verified)
	}
	want := []string{"verify", "verify-dependency-5", "verify-dependency-6", "job", "create-suspended", "assign", "resume", "close-thread", "close-image"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("子の実終了前に依存参照を解放しました: %v", api.calls)
	}
	api.emptyFail = true
	api.calls = nil
	if err := child.Wait(ctx); !errors.Is(err, ErrUnconfirmed) || !reflect.DeepEqual(api.calls, []string{"wait-process", "wait-empty"}) {
		t.Fatalf("Job未終了で依存参照を解放しました: %v %v", err, api.calls)
	}
	api.emptyFail = false
	api.calls = nil
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
	want = []string{"terminate-job", "wait-empty", "close-dependency-5", "close-dependency-6", "close-process", "close-job"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("同じ依存handleの終了後回収が不正です: %v", api.calls)
	}
}

func TestFixedCommandRejectsAmbiguousOrUnboundedInputsBeforeKernel(t *testing.T) {
	file := Artifact{Path: `C:\protected\module.psd1`, SHA256: [32]byte{2}}
	for name, input := range map[string]struct {
		args []string
		deps []Artifact
	}{
		"no args":                 {nil, nil},
		"nul":                     {[]string{"a\x00b"}, nil},
		"invalid utf8":            {[]string{string([]byte{0xff})}, nil},
		"many args":               {make([]string, 65), nil},
		"long utf16 args":         {[]string{strings.Repeat("😀", 16384)}, nil},
		"many dependencies":       {[]string{"fixed"}, make([]Artifact, 33)},
		"relative dependency":     {[]string{"fixed"}, []Artifact{{Path: "module.dll", SHA256: [32]byte{2}}}},
		"zero hash":               {[]string{"fixed"}, []Artifact{{Path: file.Path}}},
		"long dependency":         {[]string{"fixed"}, []Artifact{{Path: `C:\` + strings.Repeat("a", 32768), SHA256: file.SHA256}}},
		"invalid dependency utf8": {[]string{"fixed"}, []Artifact{{Path: "C:\\" + string([]byte{0xff}), SHA256: file.SHA256}}},
		"duplicate path":          {[]string{"fixed"}, []Artifact{file, file}},
		"case alias":              {[]string{"fixed"}, []Artifact{file, {Path: `c:\PROTECTED\MODULE.PSD1`, SHA256: [32]byte{3}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if command, err := NewFixedCommand(input.args, input.deps); command != nil || !errors.Is(err, ErrConfig) {
				t.Fatalf("不正な固定commandを受理しました: %v", err)
			}
		})
	}
	command, err := NewFixedCommand([]string{"fixed"}, []Artifact{file})
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Spec){
		"zero command": func(s *Spec) { s.Command = &FixedCommand{} },
		"bootstrap":    func(s *Spec) { s.Bootstrap = bootstrapSpec().Bootstrap },
		"image alias": func(s *Spec) {
			alias := s.Artifact
			alias.Path = strings.ToUpper(alias.Path)
			s.Command, _ = NewFixedCommand([]string{"fixed"}, []Artifact{alias})
		},
		"commandline too long": func(s *Spec) {
			s.Command, _ = NewFixedCommand([]string{strings.Repeat("a", 32760)}, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := approvedSpec()
			s.Command = command
			change(&s)
			api := &memoryKernel{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			child, err := start(ctx, s, api)
			if child != nil || !errors.Is(err, ErrConfig) || len(api.calls) != 0 {
				t.Fatalf("不正な組合せをkernel前に拒否しません: %v %v", err, api.calls)
			}
		})
	}
}

func TestFixedCommandAllowsDistinctFilesWithSameLeafOrContents(t *testing.T) {
	command, err := NewFixedCommand([]string{"fixed"}, []Artifact{
		{Path: `C:\protected\one\module.psd1`, SHA256: [32]byte{2}},
		{Path: `C:\protected\two\module.psd1`, SHA256: [32]byte{2}},
	})
	if err != nil || command == nil {
		t.Fatalf("異なる依存fileに不要な内容/leaf制約を付けました: %v", err)
	}
}

func TestFixedCommandFailedDependencyVerificationKeepsPartialHandleUntilCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &fixedKernel{memoryKernel: &memoryKernel{failures: map[string]bool{"verify-dependency-6": true, "close-dependency-6": true}}}
	child, err := start(ctx, fixedSpec(t), api)
	if child == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("部分検証失敗で未回収handleを捨てました: %v", err)
	}
	want := []string{"verify", "verify-dependency-5", "verify-dependency-6", "close-image", "close-dependency-5", "close-dependency-6"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("検証失敗後に子を起動、または所有参照を失いました: %v", api.calls)
	}
	api.failures = nil
	api.calls = nil
	if err := child.Close(ctx); err != nil || !reflect.DeepEqual(api.calls, []string{"close-dependency-6"}) {
		t.Fatalf("失敗handleだけを再回収できません: %v %v", err, api.calls)
	}
}

func TestFixedCommandCancellationDuringDependencyVerifyNeverCreatesChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &fixedKernel{memoryKernel: &memoryKernel{onCall: func(call string) {
		if call == "verify-dependency-5" {
			cancel()
		}
	}}}
	child, err := start(ctx, fixedSpec(t), api)
	want := []string{"verify", "verify-dependency-5", "close-image", "close-dependency-5"}
	if child != nil || !errors.Is(err, ErrStart) || !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("取消した依存検証から子を起動/参照破棄しました: %v %v", err, api.calls)
	}
}

func TestFixedCommandConcurrentWaitCloseReleaseDependenciesOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &fixedKernel{memoryKernel: &memoryKernel{}}
	child, err := start(ctx, fixedSpec(t), api)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
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
				t.Errorf("並行終了を確認できません: %v", err)
			}
		}(i)
	}
	workers.Wait()
	for _, close := range []string{"close-dependency-5", "close-dependency-6"} {
		count := 0
		for _, call := range api.calls {
			if call == close {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("依存handleの解放回数が不正です: %s=%d", close, count)
		}
	}
}
