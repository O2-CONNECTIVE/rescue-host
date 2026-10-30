package serviceprovision

import (
	"context"
	"errors"
	"runtime"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
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
		if err == nil && e.staging {
			err = service.BindGuestStagingOwner(e)
		}
		if err == nil && e.runtimeGuest {
			err = service.BindRuntimeGuestOwner(e)
		}
		if err == nil && e.preview != nil {
			if policyStage, ok := e.stage.(operatorSIDPolicyStage); ok {
				err = policyStage.BindOperatorSIDPolicy(ctx, service, e)
			}
		}
		if err == nil {
			e.service.Store(service)
		}
	}
	if errors.Is(err, ErrRootFatal) {
		healthy = false
	}
	if err != nil {
		// 構成済みでもまだSCM/Serveへ渡していない。inner-emptyの終了を先に確認する。
		if service != nil {
			e.closeUnpublishedService(service)
		}
		ready <- executionReady{err: err}
		select {
		case <-e.cleaned:
			return // 実outer RPCで既に回収した。Closeとackを重複しない。
		default:
		}
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
		if request.runtime != nil {
			result := e.runtimeOnOwner(request.ctx, *request.runtime, request.lease, request.closeGuest)
			request.runtimeReply <- result
			if errors.Is(result.err, ErrRootFatal) {
				healthy = false
				for {
					time.Sleep(e.cfg.CleanupRetry)
				}
			}
			continue
		}
		if request.guest != nil {
			guestErr := e.guestOnOwner(request.ctx, *request.guest, request.lease, request.closeGuest)
			request.reply <- guestErr
			if errors.Is(guestErr, ErrRootFatal) {
				healthy = false
				for {
					time.Sleep(e.cfg.CleanupRetry)
				}
			}
			continue
		}
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

// 未公開ServiceのCloseは別workerで進め、元threadが自身へのouter RPCを処理する。
// 内部参照はcleanup identityだけで、ready/SCMへ成功として公開しない。
func (e *Execution) closeUnpublishedService(service *servicehost.Service) {
	e.service.Store(service)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		e.closeService(service)
	}()
	for {
		select {
		case <-joined:
			return // outer未登録なら、呼出元が元threadでRootを回収する。
		case request := <-e.requests:
			if request.runtime != nil {
				request.runtimeReply <- runtimeGuestResult{err: ErrRootUnconfirmed}
				continue
			}
			if request.guest != nil || request.confirmation != nil || !rootLive(request.ctx) || !request.proof.Confirms(service) {
				request.reply <- ErrRootUnconfirmed
				continue
			}
			cleanup, cancel := context.WithTimeout(request.ctx, e.cfg.Anchor.CleanupTimeout)
			err := e.stage.Close(cleanup)
			cancel()
			if err == nil {
				close(e.cleaned)
			}
			request.reply <- err
			if err == nil {
				<-joined // native回収だけでなく、所有Close workerも実returnまでjoinする。
				return
			}
			if errors.Is(err, ErrRootFatal) {
				// 不明な元thread/所有参照を他threadへ作り直さず保持して退役する。
				ticker := time.NewTicker(e.cfg.CleanupRetry)
				defer ticker.Stop()
				for range ticker.C {
				}
			}
		}
	}
}
