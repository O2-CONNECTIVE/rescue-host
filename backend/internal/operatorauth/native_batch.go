package operatorauth

import (
	"context"
	"errors"
	"sync"
)

// inputは保護serviceから所有移譲する唯一のread handle。元のduplex等の
// readableな複製を外側が残す場合、このconstructorを呼んではならない。
type nativeBatchSpec struct {
	input  uintptr
	launch nativeLaunchSpec
	report reportConfig
}
type batchOwner struct {
	revoke  func() error // 同Serviceの発行拒否/失効だけ。native I/Oを入れない。
	reclaim func(context.Context) error
}
type batchReport interface {
	childWriter() uintptr
	releaseWriter(context.Context) error
	drain(context.Context) (reportResult, error)
	close(context.Context) error
}
type batchChild interface {
	resume(context.Context) error
	recheck(context.Context) error
	waitExit(context.Context) (uint32, error)
	terminate() error
	close(context.Context) error
}
type nativeBatchOps struct {
	parent        func(context.Context) error
	clean         func() error
	newReport     func(context.Context, reportConfig) (batchReport, error)
	duplicateRead func(uintptr) (uintptr, error)
	start         func(context.Context, nativeLaunchSpec) (batchChild, error)
	closeHandle   func(uintptr) error
}
type nativeBatch struct {
	mu                 sync.Mutex
	ops                nativeBatchOps
	input, readCopy    uintptr
	report             batchReport
	child              batchChild
	collector          *Collector
	ready, childClosed bool
	fatal              error
}

func (s nativeBatchSpec) valid() bool {
	l := s.launch
	return helperHandle(s.input) && s.input != l.primaryToken && l.operatorRead == 0 && l.reportWrite == 0 &&
		localHelperImage(l.imagePath) && l.imageSHA256 != ([32]byte{}) && l.maxImageBytes > 0 && l.maxImageBytes <= 1<<30 &&
		helperHandle(l.primaryToken) && l.cleanup > 0 && l.processSDDL != "" && l.threadSDDL != "" && l.helperUserSID == s.report.helperSID && s.report.valid()
}

// startBatchは1要求の固定deadlineを全起動段階で使う。値の正当主体・保護root・
// sole-read所有の供給元は別の固定provisioningで必要であり、公開入口は閉鎖中。
func startBatch(ctx context.Context, s nativeBatchSpec, owner batchOwner, ops nativeBatchOps) (b *nativeBatch, err error) {
	if !s.valid() || owner.revoke == nil || owner.reclaim == nil || ops.parent == nil || ops.clean == nil || ops.newReport == nil || ops.duplicateRead == nil || ops.start == nil || ops.closeHandle == nil {
		return nil, ErrUnconfigured
	}
	b = &nativeBatch{ops: ops, input: s.input}
	b.collector = &Collector{cleanup: s.launch.cleanup, ops: collectorOps{
		check: b.recheck, drain: b.drain, terminate: b.terminate, wait: b.wait,
		closeIO: b.closeOwned, closeProcess: b.confirmClosed,
		revoke: owner.revoke, reclaim: func(c context.Context) error {
			if e := b.cleanupGuard(); e != nil {
				return e
			}
			return owner.reclaim(c)
		},
	}}
	defer func() {
		if recover() != nil {
			err = ErrUnconfirmed
		}
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), s.launch.cleanup)
			defer cancel()
			if e := b.collector.Close(cleanup); e != nil {
				err = errors.Join(err, e)
			}
		}
	}()
	if e := b.parent(ctx); e != nil {
		return b, e
	}
	p, e := ops.newReport(ctx, s.report)
	b.report = p
	if e != nil || p == nil {
		return b, ErrDenied
	}
	if e = b.parent(ctx); e != nil {
		return b, e
	}
	b.readCopy, e = ops.duplicateRead(b.input)
	if !helperHandle(b.readCopy) || b.readCopy == b.input || b.readCopy == s.launch.primaryToken || b.readCopy == p.childWriter() {
		b.readCopy = 0
		return b, ErrDenied
	}
	if e != nil || !helperHandle(b.readCopy) || b.readCopy == b.input {
		return b, ErrDenied
	}
	if e = b.parent(ctx); e != nil {
		return b, e
	}
	l := s.launch
	l.operatorRead = b.readCopy
	l.reportWrite = p.childWriter()
	c, e := ops.start(ctx, l)
	b.child = c
	if e != nil || c == nil {
		return b, ErrDenied
	}
	if e = b.parent(ctx); e != nil {
		return b, e
	}
	if e = p.releaseWriter(ctx); e != nil {
		return b, e
	}
	if e = b.closeInputs(ctx); e != nil {
		return b, e
	}
	if e = b.parent(ctx); e != nil {
		return b, e
	}
	if e = c.resume(ctx); e != nil {
		return b, e
	}
	if e = b.parent(ctx); e != nil {
		return b, e
	}
	if e = c.recheck(ctx); e != nil {
		return b, e
	}
	if !finite(ctx) {
		return b, ErrDenied
	}
	b.ready = true
	return b, nil
}
func (b *nativeBatch) parent(ctx context.Context) error {
	if !finite(ctx) {
		return ErrDenied
	}
	if e := b.ops.parent(ctx); e != nil {
		if errors.Is(e, ErrFatal) {
			b.mu.Lock()
			b.fatal = e
			b.mu.Unlock()
		}
		return e
	}
	if !finite(ctx) {
		return ErrDenied
	}
	return nil
}
func (b *nativeBatch) cleanupGuard() error {
	b.mu.Lock()
	e := b.fatal
	b.mu.Unlock()
	if e != nil {
		return e
	}
	if e = b.ops.clean(); e != nil {
		fatal := errors.Join(ErrFatal, e)
		b.mu.Lock()
		b.fatal = fatal
		b.mu.Unlock()
		return fatal
	}
	return nil
}
func (b *nativeBatch) recheck(ctx context.Context) error {
	if e := b.parent(ctx); e != nil {
		return e
	}
	if !b.ready || b.child == nil {
		return ErrDenied
	}
	return b.child.recheck(ctx)
}
func (b *nativeBatch) drain(ctx context.Context) (reportResult, error) {
	if b.report == nil {
		return reportResult{}, ErrDenied
	}
	return b.report.drain(ctx)
}
func (b *nativeBatch) terminate() error {
	if e := b.cleanupGuard(); e != nil {
		return e
	}
	if b.child == nil {
		return nil
	}
	return b.child.terminate()
}
func (b *nativeBatch) wait(ctx context.Context) (uint32, error) {
	if e := b.cleanupGuard(); e != nil {
		return 0, e
	}
	if b.child == nil {
		return 0, nil
	}
	return b.child.waitExit(ctx)
}
func (b *nativeBatch) closeInputs(ctx context.Context) error {
	var errs []error
	for _, h := range []*uintptr{&b.readCopy, &b.input} {
		if *h == 0 {
			continue
		}
		if !finite(ctx) {
			return ErrUnconfirmed
		}
		if e := b.ops.closeHandle(*h); e != nil {
			errs = append(errs, ErrUnconfirmed)
		} else {
			*h = 0
		}
	}
	return errors.Join(errs...)
}
func (b *nativeBatch) closeOwned(ctx context.Context) error {
	if e := b.cleanupGuard(); e != nil {
		return e
	}
	if b.report != nil {
		if e := b.report.close(ctx); e != nil {
			return e
		}
	}
	if e := b.closeInputs(ctx); e != nil {
		return e
	}
	if b.child != nil {
		if e := b.child.close(ctx); e != nil {
			return e
		}
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	b.childClosed = true
	return nil
}

// Collectorの最後のcontext-free段階では新しいOS callを開始しない。
// 有限closeOwned内でJob/保持process/全I/Oの回収を確認済みの場合だけ終端とする。
func (b *nativeBatch) confirmClosed() error {
	if !b.childClosed {
		return ErrUnconfirmed
	}
	return nil
}
func (b *nativeBatch) Collect(ctx context.Context) (*Read, error) {
	if b == nil || b.collector == nil {
		return nil, ErrUnconfigured
	}
	r, e := b.collector.Collect(ctx)
	b.mu.Lock()
	fatal := b.fatal
	b.mu.Unlock()
	if fatal != nil {
		return nil, errors.Join(e, ErrFatal, fatal)
	}
	return r, e
}
func (b *nativeBatch) Close(ctx context.Context) error {
	if b == nil || b.collector == nil {
		return ErrUnconfigured
	}
	return b.collector.Close(ctx)
}
