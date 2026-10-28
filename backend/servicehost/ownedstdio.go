package servicehost

import (
	"context"
	"io"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// stdioPipes はnative所有物のOS seam。公開factoryは実Windows実装だけを選ぶ。
type stdioPipes interface {
	ChildHandles() childjob.Handles
	Input() io.WriteCloser
	Output() io.ReadCloser
	Stderr() io.ReadCloser
	ReleaseChild(context.Context) error
	Close(context.Context) error
}

// OwnedStdio は今回の子のstdioと、内容を保存しないstderr drainを単独所有する。
// Waitの成功は実stderr EOFとdrain joinだけで、process/Jobの終了証明ではない。
type OwnedStdio struct {
	mu                  sync.Mutex
	pipes               stdioPipes
	done                chan struct{}
	cleanup             chan struct{}
	limit               uint64
	eof, fault, closing bool
	authority           *supervisor.Authority
	binding             supervisor.Binding
}

func newOwnedStdio(p stdioPipes, limit uint64) (*OwnedStdio, error) {
	if p == nil || limit == 0 {
		return nil, ErrProvisioningRejected
	}
	result := &OwnedStdio{pipes: p, limit: limit, done: make(chan struct{}), cleanup: make(chan struct{}, 1)}
	result.cleanup <- struct{}{}
	if p.Stderr() == nil {
		result.fault = true
		close(result.done)
		return result, ErrProvisioningRejected
	}
	go result.drain()
	return result, nil
}

// 未完了factoryの所有物は回収だけを許し、streamを一度も読まない。
func retainedStdio(p stdioPipes, limit uint64) *OwnedStdio {
	result := &OwnedStdio{pipes: p, limit: limit, done: make(chan struct{}), cleanup: make(chan struct{}, 1), fault: true}
	result.cleanup <- struct{}{}
	close(result.done)
	return result
}

func (p *OwnedStdio) drain() {
	defer close(p.done)
	buffer := make([]byte, 4096)
	var total uint64
	for {
		n, err := p.pipes.Stderr().Read(buffer)
		if n < 0 || n > len(buffer) || uint64(n) > p.limit-total || (n == 0 && err == nil) {
			p.fail()
			return
		}
		total += uint64(n)
		if err == io.EOF {
			p.mu.Lock()
			p.eof = true
			p.mu.Unlock()
			return
		}
		if err != nil {
			p.fail()
			return
		}
	}
}

func (p *OwnedStdio) fail() {
	p.mu.Lock()
	p.fault = true
	authority, binding := p.authority, p.binding
	p.mu.Unlock()
	if authority != nil {
		_ = authority.Revoke(binding)
	}
}

// attachは同じ起動を所有するJobRuntimeだけが行う。stderrに承認権限は与えない。
func (p *OwnedStdio) attach(a *supervisor.Authority, binding supervisor.Binding) {
	p.mu.Lock()
	if p.closing || (p.authority != nil && (p.authority != a || p.binding != binding)) {
		p.mu.Unlock()
		p.fail()
		if a != nil {
			_ = a.Revoke(binding)
		}
		return
	}
	p.authority, p.binding = a, binding
	fault := p.fault
	p.mu.Unlock()
	if fault && a != nil {
		_ = a.Revoke(binding)
	}
}

func (p *OwnedStdio) Wait(ctx context.Context) error {
	if p == nil || p.done == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.eof || p.fault || ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

// Closeは資源回収であり、実EOFを確認するWaitの代用にはならない。
// 未完了workerやOS参照は保持し、同じ所有物への有限予算の再回収を許す。
func (p *OwnedStdio) Close(ctx context.Context) error {
	if p == nil || p.pipes == nil || p.done == nil {
		return ErrRuntimeUnconfirmed
	}
	p.mu.Lock()
	p.closing = true
	a, b := p.authority, p.binding
	p.mu.Unlock()
	if a != nil {
		_ = a.Revoke(b)
	}
	if !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	select {
	case <-p.cleanup:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	defer func() { p.cleanup <- struct{}{} }()
	if err := p.pipes.Close(ctx); err != nil {
		return ErrRuntimeUnconfirmed
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		return ErrRuntimeUnconfirmed
	}
	if ctx.Err() != nil {
		return ErrRuntimeUnconfirmed
	}
	return nil
}

func (p *OwnedStdio) releaseChild(ctx context.Context) error {
	if p == nil || p.pipes == nil || !controlBudget(ctx) {
		return ErrRuntimeUnconfirmed
	}
	return p.pipes.ReleaseChild(ctx)
}
