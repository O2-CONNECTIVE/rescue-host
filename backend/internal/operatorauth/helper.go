package operatorauth

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

// Runは専用helper processで一度だけ呼ぶ。read→message token照会→復帰を
// 同じ固定threadで行う。dispatch/鍵操作/要求callbackはこのscopeに入れない。
func (h *Helper) Run(ctx context.Context) (result error) {
	if h == nil {
		return ErrDenied
	}
	h.mu.Lock()
	if h.used || !h.valid() {
		h.mu.Unlock()
		return ErrDenied
	}
	h.used = true
	h.mu.Unlock()
	if !finite(ctx) {
		return ErrDenied
	}
	api := h.ops.thread
	api.lock()
	poisoned := false
	defer func() {
		if recover() != nil {
			poisoned = true
			api.abort()
			result = ErrFatal
		}
		if !poisoned {
			api.unlock()
		}
	}()
	fatal := func() error { poisoned = true; api.abort(); return ErrFatal }
	if api.clean() != nil {
		return fatal()
	}
	if h.ops.check(ctx) != nil || !finite(ctx) {
		return ErrDenied
	}
	frame, err := h.ops.read(ctx)
	if err != nil || !finite(ctx) || len(frame) == 0 || len(frame) > servicecontrol.FrameBytes {
		return ErrDenied
	}
	frame = append([]byte(nil), frame...)
	if h.ops.check(ctx) != nil || !finite(ctx) {
		return ErrDenied
	}
	if e := api.impersonate(h.ops.pipe); e != nil {
		if errors.Is(e, ErrFatal) {
			return fatal()
		}
		return ErrDenied
	}
	peer, err := helperToken(api)
	if errors.Is(err, ErrFatal) {
		return fatal()
	}
	if err != nil || peer != h.profile.operator || !finite(ctx) {
		return ErrDenied
	}
	if _, err = servicecontrol.DecodeUntrustedFrame(frame); err != nil {
		return ErrDenied
	}
	if h.ops.check(ctx) != nil || !finite(ctx) {
		return ErrDenied
	}
	if h.ops.publish(ctx, frame) != nil || !finite(ctx) {
		return ErrDenied
	}
	return nil
}

// token照会/closeの失敗・panicでもimpersonation成功後のrevertを省略しない。
// 復帰不明は結果なし・専用helper終了とし、親ownerの終了を要求しない。
func helperToken(api threadKernel) (peer tokenProjection, err error) {
	defer func() {
		if recover() != nil {
			peer = tokenProjection{}
			err = ErrFatal
		}
		if restoreHelper(api) != nil {
			peer = tokenProjection{}
			err = ErrFatal
		}
	}()
	return api.token()
}

func restoreHelper(api threadKernel) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrFatal
		}
	}()
	if api.revert() != nil || api.clean() != nil {
		return ErrFatal
	}
	return nil
}

func (h *Helper) valid() bool {
	p := h.profile.operator
	return h.ops.thread != nil && h.ops.pipe != 0 && h.ops.pipe < ^uintptr(0)-15 && h.ops.check != nil && h.ops.read != nil && h.ops.publish != nil &&
		p.user != "" && h.profile.guest.user != "" && p.user != h.profile.guest.user && p.authentication != 0 && p.kind == 2 && (p.level == 1 || p.level == 2) && p.integrity != 0 && !p.restricted && !p.appContainer
}
