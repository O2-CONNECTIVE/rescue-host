package operatorauth

import (
	"context"
	"errors"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// Collectはlaunch前から保持するreport readerを先にdrainする。wait先行で
// helperのwrite完了を塞がない。EOF・終了code・同じlaunch bindingは別に確認する。
func (c *Collector) Collect(ctx context.Context) (proof *Read, err error) {
	if c == nil {
		return nil, ErrDenied
	}
	c.mu.Lock()
	if c.used || c.failed || c.closed || !c.valid() || (c.request != nil && !reportContextValid(c.request)) {
		c.mu.Unlock()
		return nil, ErrDenied
	}
	if reportContextValid(ctx) {
		var cancel context.CancelFunc
		if c.request != nil {
			deadline, _ := c.request.Deadline()
			ctx, cancel = context.WithDeadline(ctx, deadline)
			stop := context.AfterFunc(c.request, cancel)
			c.activeCancel = func() { cancel(); stop() }
		} else {
			ctx, cancel = context.WithCancel(ctx)
			c.activeCancel = cancel
		}
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
	if !finite(ctx) {
		return nil, ErrDenied
	}
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
	if c.failed || c.closed || !reportContextValid(ctx) || (c.request != nil && !reportContextValid(c.request)) {
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
	return !c.failed && !c.closed && reportContextValid(ctx) && (c.request == nil || reportContextValid(c.request))
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
	return r.owner.Consume(ctx, r)
}

// Consumeは呼出元が保持した同じCollectorを明示してproofを消費する。
// 別ownerのproofは触らず拒否し、本人の期限/取消失敗は単回権をburnする。
func (c *Collector) Consume(ctx context.Context, r *Read) ([]byte, error) {
	if c == nil || r == nil {
		return nil, ErrDenied
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.owner != c || c.proof != r || r.used {
		return nil, ErrDenied
	}
	r.used = true
	if c.failed || c.closed || (c.request != nil && !reportContextValid(c.request)) || !reportContextValid(ctx) || !reportContextValid(r.request) || !time.Now().Before(r.deadline) {
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
		if result != nil {
			result = collectorCleanupFailure(result, c.revokeErr, c.reclaimErr)
		}
	}()
	// 初回のfatal通知後には独立helper回収も一度だけ試みた。
	// 未確認handleを保持したまま、同じownerでnative操作を再開しない。
	if collectorCleanupFatal(c.reclaimErr) {
		return c.reclaimErr
	}
	c.mu.Lock()
	activeCancel := c.activeCancel
	c.mu.Unlock()
	if activeCancel != nil {
		defer activeCancel()
	}
	if !c.revoked {
		c.revokeErr = c.ops.revoke()
		c.revoked = true
	}
	if activeCancel != nil {
		activeCancel()
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	if !c.reclaimed && !collectorCleanupFatal(c.reclaimErr) {
		// workloadのmemory待機に全予算を使わせず、同じ元deadline内で
		// 独立helperのwait/I/O回収を試す時間を残す。
		deadline, _ := ctx.Deadline()
		reclaim, endReclaim := context.WithTimeout(ctx, time.Until(deadline)/2)
		defer endReclaim()
		err := c.ops.reclaim(reclaim)
		endReclaim()
		c.reclaimed = err == nil
		if err != nil {
			c.reclaimErr = collectorCleanupFailure(err)
		} else {
			c.reclaimErr = nil
		}
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

func collectorCleanupFatal(err error) bool {
	return errors.Is(err, ErrFatal) || errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal)
}

// Callbackの内部診断を返さず、既知の終端分類だけを保持する。
func collectorCleanupFailure(causes ...error) error {
	result := ErrUnconfirmed
	for _, fatal := range []error{ErrFatal, bootstrapauth.ErrFatal, protectedkey.ErrFatal} {
		for _, cause := range causes {
			if errors.Is(cause, fatal) {
				result = errors.Join(result, fatal)
				break
			}
		}
	}
	return result
}
