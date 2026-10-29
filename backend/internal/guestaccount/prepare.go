package guestaccount

import (
	"context"
	"errors"
)

// Prepareはdisabled accountの所有記録と限定snapshot検査までを順序づける。
// SSH接続、有効化、資格情報生成、grant発行を行わない。
func (l *Lifecycle) Prepare(ctx context.Context) (result error) {
	if err := l.enter(ctx); err != nil {
		return err
	}
	defer l.finish(&result)
	if l.state != Planned || l.stop.Load() {
		return ErrDenied
	}
	l.state = Preparing
	if err := l.save(ctx, reserved, true); err != nil {
		return err
	}
	if l.stop.Load() {
		l.state = Cleaning
		return ErrCleanup
	}
	if err := l.save(ctx, createAttempt, false); err != nil {
		return err
	}
	if !finite(ctx) || l.stop.Load() {
		l.state = Cleaning
		return ErrCleanup
	}
	// attemptの永続確認後、一つのLifecycleから一度だけ作成を要求する。
	r, err := l.sources.accounts.createDisabled(ctx, l.record.binding)
	bound := r.seal == l.sources.accountSeal && r.kind == created && l.plan.validBinding(r.binding)
	if bound {
		// 部分成功のSIDも、error/cancelを見るより先に所有者へ残す。
		l.record.binding = r.binding
		if session, ok := l.sources.session.(creatorSessionBinder); ok {
			err = errors.Join(err, session.bindCreated(r, l.sources.accountSeal))
		}
	}
	// SIDを保持したまま、期限切れによる保存中止にも元の不確定性を残す。
	if err != nil {
		l.failure(err)
	}
	if bound {
		if saveErr := l.save(ctx, ownedSID, false); saveErr != nil {
			if l.unknown {
				l.state = Unknown
				return ErrUnknown
			}
			l.state = Cleaning
			return saveErr
		}
	}
	if err != nil {
		return l.failure(err)
	}
	if !bound {
		return l.failure(ErrUnknown)
	}
	if !finite(ctx) || l.stop.Load() {
		l.state = Cleaning
		return ErrCleanup
	}
	snapshot, err := l.sources.accounts.inspect(ctx, l.record.binding)
	if err != nil || !finite(ctx) || l.stop.Load() || !l.limited(snapshot) {
		l.state = Cleaning
		return ErrCleanup
	}
	if err := l.save(ctx, preparedDisabled, false); err != nil {
		return err
	}
	l.state = Prepared
	return nil
}
