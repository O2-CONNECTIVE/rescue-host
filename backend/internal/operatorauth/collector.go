package operatorauth

import (
	"context"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// Collectはlaunch前から保持するreport readerを先にdrainする。wait先行で
// helperのwrite完了を塞がない。EOF・終了code・同じlaunch bindingは別に確認する。
func (c *Collector) Collect(ctx context.Context) (proof *Read, err error) {
	if c == nil || !finite(ctx) {
		return nil, ErrDenied
	}
	c.mu.Lock()
	if c.used || c.failed || c.closed || !c.valid() {
		c.mu.Unlock()
		return nil, ErrDenied
	}
	c.used, c.busy = true, true
	c.mu.Unlock()
	defer func() {
		if recover() != nil {
			proof = nil
			err = ErrDenied
		}
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), c.cleanup)
			defer cancel()
			_ = c.Close(cleanup) // errorでも同じownerをcallerが保持する。
		}
	}()
	if c.ops.check(ctx) != nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	report, e := c.ops.drain(ctx)
	if e != nil || !report.eof || len(report.frame) == 0 || len(report.frame) > servicecontrol.FrameBytes || !c.live(ctx) {
		return nil, ErrDenied
	}
	frame := append([]byte(nil), report.frame...)
	if _, e = servicecontrol.DecodeUntrustedFrame(frame); e != nil {
		return nil, ErrDenied
	}
	exit, e := c.ops.wait(ctx)
	if e != nil {
		return nil, ErrUnconfirmed
	}
	c.mu.Lock()
	c.waited = true
	c.mu.Unlock()
	if exit != 0 || !c.live(ctx) || c.ops.check(ctx) != nil || !c.live(ctx) {
		return nil, ErrDenied
	}
	if c.ops.closeIO(ctx) != nil {
		return nil, ErrUnconfirmed
	}
	c.mu.Lock()
	c.ioClosed = true
	c.mu.Unlock()
	if c.ops.closeProcess() != nil {
		return nil, ErrUnconfirmed
	}
	c.mu.Lock()
	c.processClosed = true
	c.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed || c.closed || !finite(ctx) {
		return nil, ErrDenied
	}
	deadline, _ := ctx.Deadline()
	proof = &Read{owner: c, request: ctx, frame: frame, deadline: deadline}
	c.proof = proof
	return proof, nil
}

func (c *Collector) live(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.failed && !c.closed && finite(ctx)
}
func (c *Collector) valid() bool {
	o := c.ops
	return c.cleanup > 0 && o.check != nil && o.drain != nil && o.terminate != nil && o.wait != nil && o.closeIO != nil && o.closeProcess != nil && o.revoke != nil && o.reclaim != nil
}

// Consumeは観測/失効/停止receiptではない。同じServiceのdispatch側が、同じ
// 停止critical sectionで追加のowner/intent検査を行う必要がある。
func (r *Read) Consume(ctx context.Context) ([]byte, error) {
	if r == nil || r.owner == nil {
		return nil, ErrDenied
	}
	c := r.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.used || c.proof != r {
		return nil, ErrDenied
	}
	r.used = true // 取消・期限失敗でもburnを戻さない。
	if c.failed || c.closed || !finite(ctx) || !finite(r.request) || !time.Now().Before(r.deadline) {
		return nil, ErrDenied
	}
	return append([]byte(nil), r.frame...), nil
}

// Closeは失敗processから独立したhealthy ownerが行う。新規発行拒否/同issuer
// revokeを最初に要求し、workload回収をhelper waitより先に試みる。
// 不明時は同じCollector/held handlesを保持し、有限contextで再確認する。
func (c *Collector) Close(ctx context.Context) (result error) {
	if c == nil {
		return ErrUnconfigured
	}
	c.mu.Lock()
	c.failed, c.closed = true, true
	c.mu.Unlock()
	if !c.valid() {
		return ErrUnconfirmed
	}
	if !c.cleaning.TryLock() {
		return ErrUnconfirmed
	}
	defer c.cleaning.Unlock()
	defer func() {
		if recover() != nil {
			result = ErrUnconfirmed
		}
	}()
	if !c.revoked {
		c.revokeErr = c.ops.revoke()
		c.revoked = true
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	if !c.reclaimed {
		c.reclaimed = c.ops.reclaim(ctx) == nil
	}
	c.mu.Lock()
	busy, waited, processClosed := c.busy, c.waited, c.processClosed
	c.mu.Unlock()
	if !c.terminated && !waited && !processClosed {
		c.terminated = c.ops.terminate() == nil
	}
	if busy {
		return ErrUnconfirmed
	}
	if !c.waited {
		_, e := c.ops.wait(ctx)
		c.waited = e == nil
	}
	if !c.ioClosed {
		c.ioClosed = c.ops.closeIO(ctx) == nil
	}
	if c.waited && c.ioClosed && !c.processClosed {
		c.processClosed = c.ops.closeProcess() == nil
	}
	if c.revokeErr != nil || !c.reclaimed || !c.waited || !c.ioClosed || !c.processClosed || !finite(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
