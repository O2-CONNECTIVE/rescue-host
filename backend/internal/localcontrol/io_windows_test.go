//go:build windows

package localcontrol

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 全APIをfake化し、pipe/event/ACLの実作成や既存設定変更は行わない。
func TestOverlappedCancellationWaitsForCompletion(t *testing.T) {
	for _, completion := range []error{nil, windows.ERROR_OPERATION_ABORTED} {
		t.Run(completionName(completion), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var events []string
			var original *windows.Overlapped
			api := pipeCalls{
				event: func() (windows.Handle, error) { events = append(events, "event"); return 7, nil },
				start: func(kind ioKind, h windows.Handle, b []byte, o *windows.Overlapped) error {
					if kind != readIO || h != 9 || len(b) != 4 || o.HEvent != 7 {
						t.Fatal("invalid overlapped arguments")
					}
					original = o
					events = append(events, "start")
					cancel()
					return windows.ERROR_IO_PENDING
				},
				cancel: func(h windows.Handle, o *windows.Overlapped) error {
					if o != original || h != 9 {
						t.Fatal("cancelled another operation")
					}
					events = append(events, "cancel")
					return windows.ERROR_NOT_FOUND
				},
				wait: func(h windows.Handle, ms uint32) (uint32, error) {
					if h != 7 || ms != 10 {
						t.Fatal("invalid wait")
					}
					events = append(events, "wait")
					return windows.WAIT_OBJECT_0, nil
				},
				result: func(h windows.Handle, o *windows.Overlapped, n *uint32, wait bool) error {
					if o != original || wait {
						t.Fatal("invalid result")
					}
					events = append(events, "result")
					*n = 4
					return completion
				},
				close: func(windows.Handle) error { events = append(events, "close-event"); return nil },
				now:   time.Now, fatal: func() { t.Fatal("completed cancellation marked unsafe") },
			}
			if _, err := pipeOperation(ctx, 9, make([]byte, 4), readIO, api); err != ErrDenied {
				t.Fatal("cancel race reported success")
			}
			if !reflect.DeepEqual(events, []string{"event", "start", "cancel", "wait", "result", "close-event"}) {
				t.Fatal("event released before completion")
			}
		})
	}
}

func completionName(err error) string {
	if err == nil {
		return "completed-normally"
	}
	return "completed-aborted"
}

func TestOverlappedTerminalResultsAndConnectRace(t *testing.T) {
	for _, name := range []string{"connected", "immediate", "more-data", "broken-pipe", "short-write", "pending"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			results, waits, closed := 0, 0, 0
			kind := readIO
			if name == "connected" {
				kind = connectIO
			}
			api := pipeCalls{
				event: func() (windows.Handle, error) { return 7, nil },
				start: func(ioKind, windows.Handle, []byte, *windows.Overlapped) error {
					switch name {
					case "connected":
						return windows.ERROR_PIPE_CONNECTED
					case "more-data":
						return windows.ERROR_MORE_DATA
					case "broken-pipe":
						return windows.ERROR_BROKEN_PIPE
					case "pending":
						return windows.ERROR_IO_PENDING
					}
					return nil
				},
				wait: func(windows.Handle, uint32) (uint32, error) { waits++; return windows.WAIT_OBJECT_0, nil },
				result: func(_ windows.Handle, _ *windows.Overlapped, n *uint32, _ bool) error {
					results++
					*n = 4
					if name == "short-write" {
						*n = 1
					}
					return nil
				},
				cancel: func(windows.Handle, *windows.Overlapped) error { t.Fatal("unexpected cancel"); return nil },
				close:  func(windows.Handle) error { closed++; return nil }, now: time.Now, fatal: func() { t.Fatal("unexpected fatal") },
			}
			n, err := pipeOperation(ctx, 9, make([]byte, 4), kind, api)
			if closed != 1 {
				t.Fatal("event leaked")
			}
			if name == "more-data" || name == "broken-pipe" {
				if err != ErrDenied || results != 0 {
					t.Fatal("failed start accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "connected" {
				if n != 0 || results != 0 {
					t.Fatal("preconnected pipe waited for nonexistent completion")
				}
				return
			}
			if results != 1 || ((name == "pending") != (waits == 1)) {
				t.Fatal("completion result missing")
			}
			if name == "short-write" && n != 1 {
				t.Fatal("short count concealed")
			}
		})
	}
}

func TestOverlappedUnknownCleanupIsFatal(t *testing.T) {
	for _, name := range []string{"cancel-error", "wait-error", "incomplete", "cleanup-budget", "event-close"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			fatal := 0
			now := time.Unix(0, 0)
			api := pipeCalls{
				event: func() (windows.Handle, error) { return 7, nil },
				start: func(ioKind, windows.Handle, []byte, *windows.Overlapped) error {
					cancel()
					return windows.ERROR_IO_PENDING
				},
				cancel: func(windows.Handle, *windows.Overlapped) error {
					if name == "cancel-error" {
						return errors.New("PRIVATE_API_ERROR")
					}
					return nil
				},
				wait: func(windows.Handle, uint32) (uint32, error) {
					if name == "wait-error" {
						return 0, errors.New("PRIVATE_API_ERROR")
					}
					if name == "cleanup-budget" {
						return uint32(windows.WAIT_TIMEOUT), nil
					}
					return windows.WAIT_OBJECT_0, nil
				},
				result: func(windows.Handle, *windows.Overlapped, *uint32, bool) error {
					if name == "incomplete" {
						return windows.ERROR_IO_INCOMPLETE
					}
					return nil
				},
				close: func(windows.Handle) error {
					if name == "event-close" {
						return errors.New("PRIVATE_API_ERROR")
					}
					return nil
				},
				now: func() time.Time { v := now; now = now.Add(2 * time.Second); return v }, fatal: func() { fatal++ },
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("unsafe cleanup returned")
					}
				}()
				_, _ = pipeOperation(ctx, 9, make([]byte, 4), readIO, api)
			}()
			if fatal != 1 {
				t.Fatal("unsafe operation was not fatal")
			}
		})
	}
}

func TestBoundedReadAndCompleteWriteCounts(t *testing.T) {
	for _, name := range []string{"empty-read", "maximum-read", "overflow-count", "complete-write", "short-write", "oversize-write"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			starts := 0
			api := pipeCalls{
				event: func() (windows.Handle, error) { return 7, nil },
				start: func(kind ioKind, _ windows.Handle, b []byte, _ *windows.Overlapped) error {
					starts++
					if kind == readIO && len(b) != 16384 {
						t.Fatal("read allocation unbounded")
					}
					return nil
				},
				result: func(_ windows.Handle, _ *windows.Overlapped, n *uint32, _ bool) error {
					switch name {
					case "maximum-read":
						*n = 16384
					case "overflow-count":
						*n = 16385
					case "complete-write":
						*n = 4
					case "short-write":
						*n = 3
					}
					return nil
				},
				close: func(windows.Handle) error { return nil }, fatal: func() { t.Fatal("unexpected fatal") },
			}
			if name == "complete-write" || name == "short-write" || name == "oversize-write" {
				frame := []byte("abc\n")
				if name == "oversize-write" {
					frame = make([]byte, 16385)
				}
				err := writeMessage(ctx, 9, frame, api)
				if (err == nil) != (name == "complete-write") {
					t.Fatal("partial or oversize write accepted")
				}
				if name == "oversize-write" && starts != 0 {
					t.Fatal("oversize write issued")
				}
				return
			}
			frame, err := readMessage(ctx, 9, api)
			if (err == nil) != (name == "maximum-read") {
				t.Fatal("invalid read count accepted")
			}
			if name == "maximum-read" && len(frame) != 16384 {
				t.Fatal("maximum frame truncated")
			}
		})
	}
}
