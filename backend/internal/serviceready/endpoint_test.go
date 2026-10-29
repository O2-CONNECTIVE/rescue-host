package serviceready

import (
	"errors"
	"testing"
)

type endpointTestConnect struct {
	done, released, canceled bool
	releaseErr               error
	polls, releases          int
}

func (o *endpointTestConnect) poll() (bool, error) { o.polls++; return o.done, nil }
func (o *endpointTestConnect) cancel() error       { o.canceled = true; return nil }
func (o *endpointTestConnect) release() error {
	o.releases++
	if o.releaseErr == nil {
		o.released = true
	}
	return o.releaseErr
}

type endpointTestPipe struct {
	fakeListener
	connection           endpointTestConnect
	connects             int
	connectErr, closeErr error
	onDuplicate, onClose func()
	read, writer         endpointTestHandle
	duplicates           []uint32
	duplicateErr         error
}

func (p *endpointTestPipe) connect() (operation, error) {
	p.connects++
	return &p.connection, p.connectErr
}
func (p *endpointTestPipe) close() error {
	p.closes++
	if p.onClose != nil {
		p.onClose()
	}
	return p.closeErr
}
func (p *endpointTestPipe) duplicate(access uint32) (endpointHandle, error) {
	p.duplicates = append(p.duplicates, access)
	if p.onDuplicate != nil {
		p.onDuplicate()
	}
	if access == 0x120089 {
		return &p.read, p.duplicateErr
	}
	return &p.writer, p.duplicateErr
}

type endpointTestHandle struct {
	handle                      uintptr
	access                      uint32
	closes, checks              int
	err                         error
	closeErr, startErr, takeErr error
	onTake, onClose             func()
	write                       endpointTestWrite
	writes                      int
	buffer                      []byte
}

func (h *endpointTestHandle) validate(access uint32) error {
	h.checks++
	if h.access != access {
		return ErrDenied
	}
	return h.err
}
func (h *endpointTestHandle) take() (uintptr, error) {
	v := h.handle
	h.handle = 0
	if h.onTake != nil {
		h.onTake()
	}
	return v, h.takeErr
}
func (h *endpointTestHandle) close() error {
	h.closes++
	if h.onClose != nil {
		h.onClose()
	}
	if h.closeErr == nil {
		h.handle = 0
	}
	return h.closeErr
}
func (h *endpointTestHandle) startWrite(b []byte) (endpointWriteOperation, error) {
	h.writes++
	h.buffer = b
	return &h.write, h.startErr
}

type endpointTestWrite struct {
	done, released, canceled bool
	n                        uint32
	err, releaseErr          error
	onPoll                   func()
}

func (o *endpointTestWrite) poll() (bool, uint32, error) {
	if o.onPoll != nil {
		o.onPoll()
	}
	return o.done, o.n, o.err
}
func (o *endpointTestWrite) cancel() error { o.canceled = true; return nil }
func (o *endpointTestWrite) release() error {
	if o.releaseErr == nil {
		o.released = true
	}
	return o.releaseErr
}

type endpointTestKernel struct {
	*preflightKernel
	pipe      endpointTestPipe
	createErr error
	onCreate  func()
}

func (k *endpointTestKernel) createEndpoint(string, string) (endpointDuplex, error) {
	k.listenerCreates++
	if k.onCreate != nil {
		k.onCreate()
	}
	return &k.pipe, k.createErr
}

func endpointFixture(t *testing.T) (*Factory, *endpointTestKernel) {
	t.Helper()
	f, preflight := preflightFixture(t)
	k := &endpointTestKernel{preflightKernel: preflight}
	k.pipe.value = fixture(config()).listener.value
	k.pipe.connection.done = true
	k.pipe.read = endpointTestHandle{handle: 88, access: 0x120089}
	k.pipe.writer = endpointTestHandle{handle: 89, access: 0x2}
	k.pipe.writer.write = endpointTestWrite{done: true, n: 3}
	f.api = k
	return f, k
}

func endpointMoved(t *testing.T) (*Endpoint, *endpointTestKernel) {
	t.Helper()
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
	if h, err := s.Move(testContext(t)); h != 88 || err != nil {
		t.Fatal(h, err)
	}
	return e, k
}

func TestEndpointWriteReceiptNeedsAllBytesTerminalAndRelease(t *testing.T) {
	for _, stage := range []string{"success", "partial", "unknown terminal", "release"} {
		t.Run(stage, func(t *testing.T) {
			e, k := endpointMoved(t)
			switch stage {
			case "partial":
				k.pipe.writer.write.n = 2
			case "unknown terminal":
				k.pipe.writer.write.done = false
				k.pipe.writer.write.err = ErrUnconfirmed
			case "release":
				k.pipe.writer.write.releaseErr = ErrUnconfirmed
			}
			frame := []byte("abc")
			k.pipe.writer.write.onPoll = func() { frame[0] = 'x' }
			written, err := e.WriteResponse(testContext(t), frame)
			if stage == "success" {
				if err != nil || written == nil || !k.pipe.writer.write.released || string(k.pipe.writer.buffer) != "abc" {
					t.Fatal("全byte実writeとreleaseを確認しませんでした", err)
				}
			} else if err == nil || written != nil {
				t.Fatal("不完全writeを成功receiptにしました", err)
			}
			closed, closeErr := e.CloseEndpoint(testContext(t))
			if stage == "unknown terminal" || stage == "release" {
				if closed != nil || !errors.Is(closeErr, ErrUnconfirmed) || k.pipe.writer.closes != 0 {
					t.Fatal("未回収operationのwriterを閉鎖しました", closeErr)
				}
				k.pipe.writer.write.done, k.pipe.writer.write.err, k.pipe.writer.write.releaseErr = true, nil, nil
				closed, closeErr = e.CloseEndpoint(testContext(t))
			}
			if closeErr != nil || closed == nil || k.pipe.writer.closes != 1 || k.pipe.read.closes != 0 {
				t.Fatal("同じ物理ownerを回収しませんでした", closeErr)
			}
			if e.MatchesCompletion(written, closed) != (stage == "success") {
				t.Fatal("物理receiptの組を取り違えました")
			}
			if stage == "success" {
				copyWrite, copyClose := *written, *closed
				if e.MatchesCompletion(&copyWrite, closed) || e.MatchesCompletion(written, &copyClose) || new(Endpoint).MatchesCompletion(written, closed) {
					t.Fatal("コピー/他Endpointのreceiptを受理しました")
				}
			}
			if receipt, err := e.WriteResponse(testContext(t), []byte("abc")); receipt != nil || err == nil || k.pipe.writer.writes != 1 {
				t.Fatal("失敗/close後に再writeしました", err)
			}
		})
	}
}

func TestEndpointSplitAndMoveAreDestructiveSingleUse(t *testing.T) {
	f, k := endpointFixture(t)
	e, err := f.PrepareEndpoint(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AwaitConnected(testContext(t)); err != nil {
		t.Fatal(err)
	}
	input, err := e.Split(testContext(t))
	if err != nil || input == nil {
		t.Fatal(input, err)
	}
	if len(k.pipe.duplicates) != 2 || k.pipe.duplicates[0] != 0x120089 || k.pipe.duplicates[1] != 0x2 || k.pipe.closes != 1 || k.pipe.read.checks == 0 || k.pipe.writer.checks == 0 {
		t.Fatal("実権限を照合したread/writeへの分割とduplex閉鎖が完了していません")
	}
	copy := *input
	if h, err := copy.Move(testContext(t)); h != 0 || !errors.Is(err, ErrDenied) {
		t.Fatal("コピーから移動しました", h, err)
	}
	h, err := input.Move(testContext(t))
	if err != nil || h != 88 {
		t.Fatal("単回所有移動しませんでした", h, err)
	}
	if h, err := input.Move(testContext(t)); h != 0 || !errors.Is(err, ErrDenied) {
		t.Fatal("同じinputを再移動しました", h, err)
	}
	if _, err := e.CloseEndpoint(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.pipe.read.closes != 0 || k.pipe.writer.closes != 1 || k.pipe.closes != 1 {
		t.Fatal("移動済みreaderを閉鎖またはretained writerを漏らしました")
	}
}

func TestEndpointSplitPartialHandleStaysWithSameOwner(t *testing.T) {
	f, k := endpointFixture(t)
	e, err := f.PrepareEndpoint(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AwaitConnected(testContext(t)); err != nil {
		t.Fatal(err)
	}
	k.pipe.duplicateErr = ErrDenied
	if input, err := e.Split(testContext(t)); input != nil || err == nil {
		t.Fatal("部分取得を移譲しました", err)
	}
	k.pipe.duplicateErr = nil
	if input, err := e.Split(testContext(t)); input != nil || err == nil {
		t.Fatal("部分失敗後に分割が復活しました", err)
	}
	if k.pipe.read.closes != 0 || k.pipe.closes != 0 {
		t.Fatal("部分取得参照を早期に解放しました")
	}
	if _, err := e.CloseEndpoint(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.pipe.read.closes != 1 || k.pipe.closes != 1 || len(k.pipe.duplicates) != 1 {
		t.Fatal("部分取得した同じreaderを回収しませんでした")
	}
}

func TestEndpointConnectionRequiresCompletionAndRelease(t *testing.T) {
	for _, releaseFailure := range []bool{false, true} {
		f, k := endpointFixture(t)
		e, err := f.PrepareEndpoint(testContext(t))
		if err != nil || e == nil {
			t.Fatal(e, err)
		}
		if k.pipe.inspects != 1 {
			t.Fatal("connect前の実listenerを検査しませんでした")
		}
		if releaseFailure {
			k.pipe.connection.releaseErr = ErrUnconfirmed
		}
		err = e.AwaitConnected(testContext(t))
		if (err != nil) != releaseFailure {
			t.Fatal("connect終端だけでreleaseを代替しました", err)
		}
		if releaseFailure && (k.pipe.closes != 0 || k.pipe.inspects != 1) {
			t.Fatal("release不明のpipeを閉鎖または接続確認済み扱いしました")
		}
		if !releaseFailure && (!k.pipe.connection.released || k.pipe.inspects != 2) {
			t.Fatal("release後の実listenerを再照会しませんでした")
		}
		k.pipe.connection.releaseErr = nil
		closed, err := e.CloseEndpoint(testContext(t))
		if err != nil || closed == nil || k.pipe.closes != 1 || k.scope.closes != 1 || k.svc.closes != 1 || k.proc.closes != 1 {
			t.Fatal("同じendpointの所有参照を回収しませんでした", err)
		}
		if e.MatchesCompletion(nil, closed) || errors.Is(err, ErrFatal) {
			t.Fatal("writeなしのcloseで交換完了を主張しました")
		}
	}
}
