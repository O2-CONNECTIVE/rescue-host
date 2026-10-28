package stdio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type memoryService struct {
	calls, closes           int
	response                []byte
	handleError, closeError error
}

func (s *memoryService) Handle(ctx context.Context, frame []byte) ([]byte, error) {
	s.calls++
	if s.handleError != nil {
		return nil, s.handleError
	}
	if s.response != nil {
		return s.response, nil
	}
	return append([]byte(nil), frame...), nil
}
func (s *memoryService) Close(context.Context) error { s.closes++; return s.closeError }

type memoryOutput struct{ bytes.Buffer }

func (*memoryOutput) Close() error { return nil }

// 数字は試験専用の opaque frame であり、製品の操作や schema ではない。
func TestLoopOwnsFiniteExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	service, output := &memoryService{}, &memoryOutput{}
	err := Run(ctx, io.NopCloser(bytes.NewBufferString("1\n2\n3\n")), output, Limits{FrameBytes: 2, Frames: 3, CleanupTimeout: time.Second}, service)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "1\n2\n3\n" || service.calls != 3 || service.closes != 1 {
		t.Fatalf("exchange/cleanup mismatch: calls=%d closes=%d", service.calls, service.closes)
	}
}

func TestLoopRejectsBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, input          string
		bytes, frames, calls int
		service              memoryService
		want                 error
	}{
		{"empty", "", 2, 1, 0, memoryService{}, nil},
		{"unicode-at-limit", "あ\n", 4, 1, 1, memoryService{}, nil},
		{"unicode-over-limit", "あ\n", 3, 1, 0, memoryService{}, ErrLimit},
		{"incomplete", "1", 2, 1, 0, memoryService{}, ErrInput},
		{"over-limit", "12\n", 2, 1, 0, memoryService{}, ErrLimit},
		{"too-many", "1\n2\n", 2, 1, 1, memoryService{}, ErrLimit},
		{"service-secret", "1\n", 2, 1, 1, memoryService{handleError: errors.New("SECRET_TEST_ERROR")}, ErrService},
		{"response-limit", "1\n", 2, 1, 1, memoryService{response: []byte("12\n")}, ErrOutput},
		{"response-lines", "1\n", 4, 1, 1, memoryService{response: []byte("\n\n")}, ErrOutput},
		{"response-incomplete", "1\n", 2, 1, 1, memoryService{response: []byte("1")}, ErrOutput},
		{"cleanup-failure", "", 2, 1, 0, memoryService{closeError: errors.New("SECRET_TEST_CLOSE")}, ErrCleanup},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			output := &memoryOutput{}
			err := Run(ctx, io.NopCloser(bytes.NewBufferString(test.input)), output, Limits{FrameBytes: test.bytes, Frames: test.frames, CleanupTimeout: time.Second}, &test.service)
			if err != test.want || test.service.calls != test.calls || test.service.closes != 1 {
				t.Fatalf("boundary result mismatch: err=%v calls=%d closes=%d", err, test.service.calls, test.service.closes)
			}
			if bytes.Contains(output.Bytes(), []byte("SECRET_TEST")) {
				t.Fatal("raw error reached stdout")
			}
		})
	}
}

func TestLoopCancellationUnblocksOwnedPipes(t *testing.T) {
	for _, blocked := range []string{"read", "write"} {
		t.Run(blocked, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			service := &memoryService{}
			var input io.ReadCloser = reader
			var output io.WriteCloser = &memoryOutput{}
			if blocked == "write" {
				input = io.NopCloser(bytes.NewBufferString("1\n"))
				output = writer
			}
			err := Run(ctx, input, output, Limits{FrameBytes: 2, Frames: 1, CleanupTimeout: time.Second}, service)
			if !errors.Is(err, context.DeadlineExceeded) || service.closes != 1 {
				t.Fatal("cancel/cleanup not confirmed")
			}
		})
	}
}

func TestLoopRequiresExplicitBudget(t *testing.T) {
	service := &memoryService{}
	err := Run(context.Background(), io.NopCloser(bytes.NewBuffer(nil)), &memoryOutput{}, Limits{FrameBytes: 2, Frames: 1, CleanupTimeout: time.Second}, service)
	if err != ErrConfig || service.closes != 0 {
		t.Fatal("invalid setup transferred ownership")
	}
}
