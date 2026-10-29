package serviceready

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestEndpointWritePhaseAndFrameLimit(t *testing.T) {
	for _, phase := range []string{"preconnect", "connected", "split", "empty", "oversize", "one byte", "16KiB"} {
		t.Run(phase, func(t *testing.T) {
			f, k := endpointFixture(t)
			e, err := f.PrepareEndpoint(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if phase != "preconnect" {
				if err := e.AwaitConnected(testContext(t)); err != nil {
					t.Fatal(err)
				}
			}
			if phase != "preconnect" && phase != "connected" {
				s, err := e.Split(testContext(t))
				if err != nil {
					t.Fatal(err)
				}
				if phase != "split" {
					if _, err := s.Move(testContext(t)); err != nil {
						t.Fatal(err)
					}
				}
			}
			frame := []byte("abc")
			switch phase {
			case "empty":
				frame = nil
			case "oversize":
				frame = make([]byte, 16385)
			case "one byte":
				frame = []byte("a")
			case "16KiB":
				frame = make([]byte, 16384)
			}
			k.pipe.writer.write.n = uint32(len(frame))
			receipt, err := e.WriteResponse(testContext(t), frame)
			allowed := phase == "one byte" || phase == "16KiB"
			if (receipt != nil && err == nil) != allowed || (k.pipe.writer.writes == 1) != allowed {
				t.Fatal("writeの物理phase/上限を誤りました", err)
			}
			closed, err := e.CloseEndpoint(testContext(t))
			if err != nil || closed == nil || e.MatchesCompletion(receipt, closed) != allowed {
				t.Fatal("phase違反を正常完了へ昇格しました", err)
			}
		})
	}
}

func TestEndpointCopiesAndZeroValuesCannotAffectOriginal(t *testing.T) {
	e, k := endpointMoved(t)
	var copy Endpoint
	// Intentional value copy through reflection exercises the self guard without
	// asking go vet to endorse copying the embedded atomic values in real callers.
	reflect.ValueOf(&copy).Elem().Set(reflect.ValueOf(e).Elem())
	checks := k.scope.checks
	for _, invalid := range []*Endpoint{nil, new(Endpoint), &copy} {
		if err := invalid.AwaitConnected(testContext(t)); !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
		if s, err := invalid.Split(testContext(t)); s != nil || !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
		if r, err := invalid.WriteResponse(testContext(t), []byte("abc")); r != nil || !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
		if c, err := invalid.CloseEndpoint(testContext(t)); c != nil || !errors.Is(err, ErrDenied) || !errors.Is(err, ErrUnconfirmed) {
			t.Fatal(err)
		}
	}
	for _, input := range []*SoleInput{nil, new(SoleInput)} {
		if h, err := input.Move(testContext(t)); h != 0 || !errors.Is(err, ErrDenied) {
			t.Fatal(h, err)
		}
	}
	if k.scope.checks != checks || k.pipe.writer.closes != 0 {
		t.Fatal("コピーがcanonical ownerへ触れました")
	}
	r, err := e.WriteResponse(testContext(t), []byte("abc"))
	if err != nil {
		t.Fatal("コピー拒否がcanonical ownerを失効しました", err)
	}
	c, err := e.CloseEndpoint(testContext(t))
	if err != nil || !e.MatchesCompletion(r, c) {
		t.Fatal(err)
	}
}

func TestEndpointDeadlineDuringAcquisitionDoesNotStartConnect(t *testing.T) {
	f, k := endpointFixture(t)
	ctx := &witnessDeadline{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
	k.onCreate = func() { ctx.deadline = time.Now().Add(-time.Second) }
	e, err := f.PrepareEndpoint(ctx)
	if e == nil || !errors.Is(err, ErrDenied) || k.pipe.connects != 0 {
		t.Fatal("期限後にconnectを開始しました", err, k.pipe.connects)
	}
	if err := e.AwaitConnected(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("期限失敗後に復活しました", err)
	}
	if _, err := e.CloseEndpoint(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointMoveReturnsOwnedHandleWithLateDeadlineError(t *testing.T) {
	f, k := endpointFixture(t)
	e, err := f.PrepareEndpoint(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AwaitConnected(testContext(t)); err != nil {
		t.Fatal(err)
	}
	s, err := e.Split(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := &witnessDeadline{Context: context.Background(), deadline: time.Now().Add(time.Hour)}
	k.pipe.read.onTake = func() { ctx.deadline = time.Now().Add(-time.Second) }
	h, err := s.Move(ctx)
	if h != 88 || !errors.Is(err, ErrDenied) {
		t.Fatal("error併返の移動所有権を失いました", h, err)
	}
	if _, err := e.CloseEndpoint(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.pipe.read.closes != 0 {
		t.Fatal("移動した読取handleを回収しました")
	}
}

func TestEndpointCloseStopsInflightWriteAndRetainsOwner(t *testing.T) {
	e, k := endpointMoved(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	k.pipe.writer.write.onPoll = func() { once.Do(func() { close(entered) }); <-release }
	type outcome struct {
		receipt *WriteReceipt
		err     error
	}
	result := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { r, err := e.WriteResponse(ctx, []byte("abc")); result <- outcome{r, err} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("writeへ入りませんでした")
	}
	short, stop := context.WithTimeout(ctx, 5*time.Millisecond)
	c, err := e.CloseEndpoint(short)
	stop()
	if c != nil || !errors.Is(err, ErrUnconfirmed) || k.pipe.writer.closes != 0 {
		t.Fatal("inflight writerを閉鎖済み扱いしました", err)
	}
	unblock()
	got := <-result
	if got.receipt != nil || !errors.Is(got.err, ErrDenied) {
		t.Fatal("stop後の遅延writeを受理しました", got.err)
	}
	if c, err := e.CloseEndpoint(testContext(t)); c == nil || err != nil || e.MatchesCompletion(got.receipt, c) {
		t.Fatal("強制cleanupを正常交換へ昇格しました", err)
	}
}

func TestEndpointUnknownHandleCloseAndForeignThreadRetainReferences(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		e, k := endpointMoved(t)
		r, err := e.WriteResponse(testContext(t), []byte("abc"))
		if err != nil {
			t.Fatal(err)
		}
		if foreign {
			k.thread++
		} else {
			k.pipe.writer.closeErr = ErrUnconfirmed
		}
		c, err := e.CloseEndpoint(testContext(t))
		if c != nil || !errors.Is(err, ErrUnconfirmed) || errors.Is(err, ErrFatal) != foreign {
			t.Fatal("unknown/Fatalの回収を受理しました", err)
		}
		if foreign && k.pipe.writer.closes != 0 {
			t.Fatal("foreign threadからhandleを回収しました")
		}
		if foreign {
			k.thread--
		} else {
			k.pipe.writer.closeErr = nil
		}
		c, err = e.CloseEndpoint(testContext(t))
		if errors.Is(err, ErrFatal) != foreign || errors.Is(err, ErrUnconfirmed) || (c == nil) != foreign {
			t.Fatal("再回収でFatal/unknownを取り違えました", err)
		}
		if e.MatchesCompletion(r, c) {
			t.Fatal("unknown/foreign close後に正常交換へ復活しました")
		}
	}
}

func TestEndpointPendingConnectRetainsPipeUntilActualTerminal(t *testing.T) {
	f, k := endpointFixture(t)
	k.pipe.connection.done = false
	e, err := f.PrepareEndpoint(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	err = e.AwaitConnected(ctx)
	cancel()
	if !errors.Is(err, ErrDenied) || k.pipe.connection.released {
		t.Fatal("pending connectを確認済み扱いしました", err)
	}
	c, err := e.CloseEndpoint(testContext(t))
	if c != nil || !errors.Is(err, ErrUnconfirmed) || !k.pipe.connection.canceled || k.pipe.closes != 0 || k.scope.closes != 1 || k.svc.closes != 1 || k.proc.closes != 1 {
		t.Fatal("pending pipeを捨てたか独立query回収を妨げました", err)
	}
	k.pipe.connection.done = true
	c, err = e.CloseEndpoint(testContext(t))
	if c == nil || err != nil || !k.pipe.connection.released || k.pipe.closes != 1 || e.MatchesCompletion(nil, c) {
		t.Fatal("同じoperationの終端/解放を回収しませんでした", err)
	}
}

func TestEndpointSplitKeepsEveryPartialDuplicateAndOriginal(t *testing.T) {
	for _, stage := range []string{"read rights", "writer acquisition", "writer rights", "duplex close"} {
		t.Run(stage, func(t *testing.T) {
			f, k := endpointFixture(t)
			e, err := f.PrepareEndpoint(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := e.AwaitConnected(testContext(t)); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "read rights":
				k.pipe.read.access |= 2
			case "writer acquisition":
				k.pipe.onDuplicate = func() {
					if len(k.pipe.duplicates) == 2 {
						k.pipe.duplicateErr = ErrDenied
					}
				}
			case "writer rights":
				k.pipe.writer.access = 0x120102
			case "duplex close":
				k.pipe.closeErr = ErrUnconfirmed
			}
			if s, err := e.Split(testContext(t)); s != nil || err == nil {
				t.Fatal("不完全な分割を受理しました", err)
			}
			duplicates := len(k.pipe.duplicates)
			if s, err := e.Split(testContext(t)); s != nil || err == nil || len(k.pipe.duplicates) != duplicates {
				t.Fatal("失敗後に分割を再開しました", err)
			}
			k.pipe.closeErr = nil
			if _, err := e.CloseEndpoint(testContext(t)); err != nil {
				t.Fatal(err)
			}
			if k.pipe.read.closes != 1 || (k.pipe.writer.closes == 1) != (stage != "read rights") {
				t.Fatal("部分取得したduplicateを漏らしました")
			}
		})
	}
}
