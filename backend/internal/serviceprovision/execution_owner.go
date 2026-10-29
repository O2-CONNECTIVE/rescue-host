package serviceprovision

import (
	"context"
	"errors"
	"runtime"
	"time"
)

func (e *Execution) own(ctx context.Context, factory func() executionStage, ready chan<- executionReady) {
	runtime.LockOSThread()
	healthy := true
	defer func() {
		if healthy {
			runtime.UnlockOSThread()
		}
	}()
	e.stage = factory()
	service, err := e.stage.Initialize(ctx)
	if errors.Is(err, ErrRootFatal) {
		healthy = false
	}
	if err == nil && service == nil {
		err = ErrRootDenied
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = service.BindOuterFinalizer(e)
		if err == nil {
			e.service.Store(service)
		}
	}
	if err != nil {
		// 構成済みでもまだSCM/Serveへ渡していない。inner-emptyの終了を先に確認する。
		if service != nil {
			e.closeService(service)
		}
		ready <- executionReady{err: err}
		ticker := time.NewTicker(e.cfg.CleanupRetry)
		defer ticker.Stop()
		for {
			cleanup, cancel := context.WithTimeout(context.Background(), e.cfg.Anchor.CleanupTimeout)
			closeErr := e.stage.Close(cleanup)
			cancel()
			if closeErr == nil {
				close(e.cleaned)
				return
			}
			if errors.Is(closeErr, ErrRootFatal) {
				healthy = false
			}
			// Fatal後は新しいnative workへ移さず、この元threadとownerを保持する。
			for !healthy {
				<-ticker.C
			}
			<-ticker.C
		}
	}
	ready <- executionReady{service: service}
	for request := range e.requests {
		if request.confirmation != nil {
			resolveErr := e.resolvePreviewOnOwner(request.ctx, service, *request.confirmation)
			request.reply <- resolveErr // buffered; canceled caller cannot hold up cleanup
			if errors.Is(resolveErr, ErrRootFatal) {
				healthy = false
				// unknown/fatal ownerを他threadへ移さず、参照を保持して退役する。
				for {
					time.Sleep(e.cfg.CleanupRetry)
				}
			}
			continue
		}
		if !rootLive(request.ctx) || !request.proof.Confirms(service) {
			request.reply <- ErrRootUnconfirmed
			continue
		}
		cleanup, cancel := context.WithTimeout(request.ctx, e.cfg.Anchor.CleanupTimeout)
		closeErr := e.stage.Close(cleanup)
		cancel()
		if closeErr == nil {
			// 実回収後にctx期限が来ても、cleanedは事実として保持する。SCM側はfresh RPCで再確認する。
			close(e.cleaned)
			request.reply <- nil
			return
		}
		request.reply <- closeErr
		if errors.Is(closeErr, ErrRootFatal) {
			healthy = false
			for {
				time.Sleep(e.cfg.CleanupRetry)
			}
		}
	}
}
