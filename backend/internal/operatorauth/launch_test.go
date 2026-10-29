package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryLaunchKernel struct {
	events  []string
	fail    string
	binding helperBinding
	after   func(string)
}

func (k *memoryLaunchKernel) step(s string) error {
	k.events = append(k.events, s)
	if k.after != nil {
		k.after(s)
	}
	if k.fail == s {
		return ErrDenied
	}
	return nil
}
func (k *memoryLaunchKernel) validate(context.Context, nativeLaunchSpec) error {
	return k.step("validate")
}
func (k *memoryLaunchKernel) pinImage(context.Context, nativeLaunchSpec) (uintptr, error) {
	return 10, k.step("image")
}
func (k *memoryLaunchKernel) newJob() (uintptr, error) { return 11, k.step("job") }
func (k *memoryLaunchKernel) createSuspended(nativeLaunchSpec) (uintptr, uintptr, error) {
	return 12, 13, k.step("create")
}
func (k *memoryLaunchKernel) inspect(context.Context, uintptr, uintptr, nativeLaunchSpec) (helperBinding, error) {
	return k.binding, k.step("inspect")
}
func (k *memoryLaunchKernel) assign(uintptr, uintptr) error  { return k.step("assign") }
func (k *memoryLaunchKernel) resume(uintptr) error           { return k.step("resume") }
func (k *memoryLaunchKernel) terminateJob(uintptr) error     { return k.step("terminateJob") }
func (k *memoryLaunchKernel) terminateProcess(uintptr) error { return k.step("terminateProcess") }
func (k *memoryLaunchKernel) waitProcess(context.Context, uintptr) (uint32, error) {
	return 0, k.step("wait")
}
func (k *memoryLaunchKernel) waitEmpty(context.Context, uintptr) error { return k.step("empty") }
func (k *memoryLaunchKernel) close(uintptr) error                      { return k.step("close") }
func launchFixture() (context.Context, context.CancelFunc, nativeLaunchSpec, *memoryLaunchKernel) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	spec := nativeLaunchSpec{imagePath: `C:\Protected\helper.exe`, imageSHA256: [32]byte{1}, launchID: strings.Repeat("a", 64), maxImageBytes: 1024, primaryToken: 20, operatorRead: 21, reportWrite: 22, profileRead: 23, cleanup: time.Second, processSDDL: "D:P(A;;GA;;;SY)", threadSDDL: "D:P(A;;GA;;;SY)", helperUserSID: "S-1-5-21-1"}
	return ctx, cancel, spec, &memoryLaunchKernel{binding: helperBinding{pid: 23, creation: 24, tokenID: 25, childModifiedID: 26, sourceTokenID: 27, sourceModifiedID: 28, user: spec.helperUserSID, imagePath: spec.imagePath}}
}
func TestHelperLaunchReturnsSuspendedAndRetainsBindingAfterExit(t *testing.T) {
	ctx, cancel, spec, k := launchFixture()
	defer cancel()
	child, err := startHelper(ctx, spec, k)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(k.events, []string{"validate", "image", "job", "create", "inspect", "assign"}) {
		t.Fatal(k.events)
	}
	if err = child.resume(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = child.waitExit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = child.recheck(ctx); err != nil {
		t.Fatal(err)
	}
	for _, event := range k.events {
		if event == "close" {
			t.Fatal("wait/recheck closed retained handles")
		}
	}
	if err = child.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHelperLaunchRejectsUnsafeConfigurationBeforeKernel(t *testing.T) {
	ctx, cancel, spec, k := launchFixture()
	defer cancel()
	spec.imagePath = `C:\Protected\..\helper.exe`
	if _, err := startHelper(ctx, spec, k); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if len(k.events) != 0 {
		t.Fatal(k.events)
	}
}

func TestHelperLaunchStopsAllocationWhenPinningExpires(t *testing.T) {
	ctx, cancel, spec, k := launchFixture()
	defer cancel()
	k.after = func(step string) {
		if step == "image" {
			cancel()
		}
	}
	child, err := startHelper(ctx, spec, k)
	if !errors.Is(err, ErrDenied) || child != nil {
		t.Fatalf("child=%v err=%v", child, err)
	}
	if !reflect.DeepEqual(k.events, []string{"validate", "image", "close"}) {
		t.Fatal(k.events)
	}
}

func TestHelperCloseRetainsReferencesWhenEmptyConfirmationExpires(t *testing.T) {
	ctx, cancel, spec, k := launchFixture()
	defer cancel()
	child, err := startHelper(ctx, spec, k)
	if err != nil {
		t.Fatal(err)
	}
	k.events = nil
	k.after = func(step string) {
		if step == "empty" {
			cancel()
		}
	}
	if err = child.close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(k.events, []string{"wait", "empty"}) {
		t.Fatal(k.events)
	}
	if child.process != 12 || child.job != 11 || child.image != 10 || child.thread != 13 {
		t.Fatal("ownership discarded before confirmation")
	}
}

// These fault cases are post-implementation regressions, separate from the
// red/green slices recorded in the handoff.
func TestHelperLaunchAcquisitionFailuresAreReapedBeforeClose(t *testing.T) {
	for _, step := range []string{"image", "job", "create", "inspect", "assign"} {
		t.Run(step, func(t *testing.T) {
			ctx, cancel, spec, k := launchFixture()
			defer cancel()
			k.fail = step
			child, err := startHelper(ctx, spec, k)
			if !errors.Is(err, ErrDenied) || child != nil {
				t.Fatalf("child=%v err=%v", child, err)
			}
			wait, empty, firstClose := -1, -1, -1
			for i, event := range k.events {
				switch event {
				case "resume":
					t.Fatal("failed child resumed")
				case "wait":
					wait = i
				case "empty":
					empty = i
				case "close":
					if firstClose < 0 {
						firstClose = i
					}
				}
			}
			if firstClose < 0 {
				t.Fatal("partial acquisition leaked", k.events)
			}
			if step == "create" || step == "inspect" || step == "assign" {
				if wait < 0 || empty < wait || firstClose < empty {
					t.Fatal(k.events)
				}
			}
		})
	}
}
func TestHelperFailedReapReturnsSameOwnerForRetry(t *testing.T) {
	ctx, cancel, spec, k := launchFixture()
	defer cancel()
	k.fail = "assign"
	k.after = func(step string) {
		if step == "terminateProcess" {
			k.fail = "wait"
		}
	}
	child, err := startHelper(ctx, spec, k)
	if child == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("child=%v err=%v", child, err)
	}
	for _, event := range k.events {
		if event == "close" {
			t.Fatal("closed before process exit confirmation")
		}
	}
	k.fail = ""
	k.after = nil
	k.events = nil
	if err = child.close(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(k.events, []string{"wait", "empty", "close", "close", "close", "close"}) {
		t.Fatal(k.events)
	}
}
func TestHelperBindingDriftDeniesResumeAndPostExitRecheck(t *testing.T) {
	for _, field := range []string{"pid", "creation", "token", "childModified", "sourceToken", "sourceModified", "image", "user"} {
		t.Run(field, func(t *testing.T) {
			ctx, cancel, spec, k := launchFixture()
			defer cancel()
			child, err := startHelper(ctx, spec, k)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = child.waitExit(ctx); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "pid":
				k.binding.pid++
			case "creation":
				k.binding.creation++
			case "token":
				k.binding.tokenID++
			case "childModified":
				k.binding.childModifiedID++
			case "sourceToken":
				k.binding.sourceTokenID++
			case "sourceModified":
				k.binding.sourceModifiedID++
			case "image":
				k.binding.imagePath = `C:\Other\helper.exe`
			case "user":
				k.binding.user = "S-1-5-21-2"
			}
			if !errors.Is(child.recheck(ctx), ErrDenied) || !errors.Is(child.resume(ctx), ErrDenied) {
				t.Fatal("binding drift accepted")
			}
			for _, event := range k.events {
				if event == "resume" {
					t.Fatal("drifted child resumed")
				}
			}
		})
	}
}
func TestHelperUnconfirmedCloseRetainsOnlyUnclosedReferences(t *testing.T) {
	ctx, cancel, spec, k := launchFixture()
	defer cancel()
	child, err := startHelper(ctx, spec, k)
	if err != nil {
		t.Fatal(err)
	}
	k.fail = "close"
	if err = child.close(ctx); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if child.process != 12 || child.job != 11 || child.thread != 13 || child.image != 10 {
		t.Fatal("failed close lost handle")
	}
	k.fail = ""
	if err = child.close(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestHelperLaunchUnsafeSpecsDoNotReachKernel(t *testing.T) {
	for _, path := range []string{`helper.exe`, `\\server\share\helper.exe`, `\\?\C:\helper.exe`, `C:\a\..\helper.exe`, `C:\a\helper.exe:stream`, `C:\a.\helper.exe`, `C:/a/helper.exe`, `C:\a\\helper.exe`, `C:\helper.dll`} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel, spec, k := launchFixture()
			defer cancel()
			spec.imagePath = path
			if _, err := startHelper(ctx, spec, k); !errors.Is(err, ErrDenied) || len(k.events) != 0 {
				t.Fatal(err, k.events)
			}
		})
	}
}

func TestHelperContendedMethodsRespectCallerDeadlineAndRetainOwner(t *testing.T) {
	for _, method := range []string{"close", "resume", "recheck", "waitExit"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel, spec, k := launchFixture()
			defer cancel()
			child, err := startHelper(ctx, spec, k)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var gate sync.Once
			k.after = func(step string) {
				if step == "wait" {
					gate.Do(func() { close(entered); <-release })
				}
			}
			heldDone := make(chan error, 1)
			go func() {
				if method == "waitExit" {
					heldDone <- child.close(ctx)
				} else {
					_, e := child.waitExit(ctx)
					heldDone <- e
				}
			}()
			<-entered
			short, stop := context.WithTimeout(ctx, 15*time.Millisecond)
			defer stop()
			done := make(chan error, 1)
			go func() {
				switch method {
				case "close":
					done <- child.close(short)
				case "resume":
					done <- child.resume(short)
				case "recheck":
					done <- child.recheck(short)
				case "waitExit":
					_, e := child.waitExit(short)
					done <- e
				}
			}()
			var callErr error
			expired := false
			select {
			case callErr = <-done:
			case <-time.After(100 * time.Millisecond):
				expired = true
			}
			close(release)
			if expired {
				callErr = <-done
			}
			if e := <-heldDone; e != nil {
				t.Fatal(e)
			}
			if expired {
				t.Fatal("method waited past finite context behind held kernel operation")
			}
			if callErr == nil {
				t.Fatal("contended operation accepted after deadline")
			}
			if err = child.close(ctx); err != nil {
				t.Fatal("same owner failed retry", err)
			}
		})
	}
}

func TestHelperTerminateRejectsContendedOwnerImmediately(t *testing.T) {
	ctx, cancel, spec, k := launchFixture()
	defer cancel()
	child, err := startHelper(ctx, spec, k)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	k.after = func(step string) {
		if step == "wait" {
			close(entered)
			<-release
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- child.close(ctx) }()
	<-entered
	done := make(chan error, 1)
	go func() { done <- child.terminate() }()
	var callErr error
	blocked := false
	select {
	case callErr = <-done:
	case <-time.After(100 * time.Millisecond):
		blocked = true
	}
	close(release)
	if blocked {
		callErr = <-done
	}
	if err = <-closed; err != nil {
		t.Fatal(err)
	}
	if blocked || !errors.Is(callErr, ErrUnconfirmed) {
		t.Fatalf("blocked=%v err=%v", blocked, callErr)
	}
	if err = child.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHelperLaunchPanicRetainsPartialOwner(t *testing.T) {
	ctx, cancel, s, k := launchFixture()
	defer cancel()
	k.after = func(step string) {
		if step == "inspect" {
			panic("synthetic query fault")
		}
	}
	var child *helperChild
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = ErrUnconfirmed
			}
		}()
		child, err = startHelper(ctx, s, k)
	}()
	if child == nil || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("partial owner lost: child=%v err=%v", child, err)
	}
	if child.process != 12 || child.thread != 13 || child.job != 11 || child.image != 10 {
		t.Fatal("partial references lost")
	}
	k.after = nil
	if err = child.terminate(); err != nil {
		t.Fatal(err)
	}
	if err = child.close(ctx); err != nil {
		t.Fatal(err)
	}
}
