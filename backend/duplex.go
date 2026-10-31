package backend

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// 片方向の有限bufferだけを持つmemory経路。socket、listener、名前空間を作らない。
const duplexBuffer = 64 << 10

type halfPipe struct {
	mu                          sync.Mutex
	changed                     chan struct{}
	buf                         []byte
	writeClosed, readClosed     bool // 各端のClose/CloseWrite
	broken                      bool // 独立停止。残りのbufferも捨てる。
	readDeadline, writeDeadline time.Time
}

func newHalfPipe() *halfPipe { return &halfPipe{changed: make(chan struct{})} }

func (p *halfPipe) notifyLocked() { close(p.changed); p.changed = make(chan struct{}) }

// waitLocked はlockを外して状態変化か期限まで待ち、再取得して戻る。
func (p *halfPipe) waitLocked(deadline time.Time) {
	changed := p.changed
	p.mu.Unlock()
	defer p.mu.Lock()
	if deadline.IsZero() {
		<-changed
		return
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-changed:
	case <-timer.C:
	}
}

func expired(deadline time.Time) bool { return !deadline.IsZero() && !time.Now().Before(deadline) }

func (p *halfPipe) read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		switch {
		case p.broken || p.readClosed:
			return 0, net.ErrClosed
		case len(p.buf) > 0:
			n := copy(b, p.buf)
			p.buf = p.buf[n:]
			p.notifyLocked()
			return n, nil
		case p.writeClosed:
			return 0, io.EOF
		case len(b) == 0:
			return 0, nil
		case expired(p.readDeadline):
			return 0, os.ErrDeadlineExceeded
		}
		p.waitLocked(p.readDeadline)
	}
}

func (p *halfPipe) write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	written := 0
	for written < len(b) {
		switch {
		case p.broken || p.writeClosed:
			return written, net.ErrClosed
		case p.readClosed:
			return written, io.ErrClosedPipe
		case len(p.buf) < duplexBuffer:
			n := min(duplexBuffer-len(p.buf), len(b)-written)
			p.buf = append(p.buf, b[written:written+n]...)
			written += n
			p.notifyLocked()
			continue
		case expired(p.writeDeadline):
			return written, os.ErrDeadlineExceeded
		}
		p.waitLocked(p.writeDeadline)
	}
	return written, nil
}

func (p *halfPipe) update(f func()) { p.mu.Lock(); defer p.mu.Unlock(); f(); p.notifyLocked() }

// ownedDuplex は同じSessionの内側endpointが生成した1組だけを表す。
// 外部から構築・注入する公開経路はなく、ownerが一致しない端は内側SSHへ渡さない。
type ownedDuplex struct {
	owner          *innerEndpoint
	toServer       *halfPipe
	toClient       *halfPipe
	client, server *duplexEnd
}

type duplexEnd struct {
	pair    *ownedDuplex
	in, out *halfPipe
}

func newOwnedDuplex(owner *innerEndpoint) *ownedDuplex {
	d := &ownedDuplex{owner: owner, toServer: newHalfPipe(), toClient: newHalfPipe()}
	d.client = &duplexEnd{pair: d, in: d.toClient, out: d.toServer}
	d.server = &duplexEnd{pair: d, in: d.toServer, out: d.toClient}
	return d
}

// cutoff は両方向を同時に破棄する独立停止経路。memory上の停止であり、物理TCP遮断ではない。
func (d *ownedDuplex) cutoff() error {
	for _, p := range []*halfPipe{d.toServer, d.toClient} {
		p.update(func() { p.broken, p.buf = true, nil })
	}
	return nil
}

func (e *duplexEnd) Read(b []byte) (int, error)  { return e.in.read(b) }
func (e *duplexEnd) Write(b []byte) (int, error) { return e.out.write(b) }

// CloseWrite は送信方向だけを閉じ、相手は残りを読み終えてからEOFを受ける。
func (e *duplexEnd) CloseWrite() error { e.out.update(func() { e.out.writeClosed = true }); return nil }

// Close は冪等で、相手の後続Writeを失敗させる。相手は送信済みbufferを読み切れる。
func (e *duplexEnd) Close() error {
	e.out.update(func() { e.out.writeClosed = true })
	e.in.update(func() { e.in.readClosed = true })
	return nil
}

type duplexAddr struct{}

func (duplexAddr) Network() string { return "rescue-owned-inner" }
func (duplexAddr) String() string  { return "rescue-owned-inner" }

func (e *duplexEnd) LocalAddr() net.Addr  { return duplexAddr{} }
func (e *duplexEnd) RemoteAddr() net.Addr { return duplexAddr{} }
func (e *duplexEnd) SetDeadline(t time.Time) error {
	_ = e.SetReadDeadline(t)
	return e.SetWriteDeadline(t)
}
func (e *duplexEnd) SetReadDeadline(t time.Time) error {
	e.in.update(func() { e.in.readDeadline = t })
	return nil
}
func (e *duplexEnd) SetWriteDeadline(t time.Time) error {
	e.out.update(func() { e.out.writeDeadline = t })
	return nil
}
