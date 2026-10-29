package guestaccount

import (
	"context"
)

// Closeは旧sessionの失効を先に要求する。削除結果不明時は照合だけを行う。
func (l *Lifecycle) Close(ctx context.Context) (result error) {
	if !l.valid() {
		return ErrUnconfigured
	}
	// source呼出し中はlocal admissionだけを閉じる。actual revokeはgate後。
	// Service統合には本workerと独立したcanonical authority失効が必要。
	l.stop.Store(true)
	if !finite(ctx) {
		return ErrDenied
	}
	if err := l.enter(ctx); err != nil {
		return err
	}
	defer l.finish(&result)
	if l.state == Closed {
		return nil
	}
	if l.deletionObserved {
		return l.finishDeletion(ctx)
	}
	if l.record.stage == abortComplete {
		// receipt確認後のcaller取消では、確定済み終端を再保存しない。
		l.state = Closed
		return nil
	}
	wasPlanned := l.state == Planned
	l.state = Cleaning
	r, revErr := l.revoke(ctx)
	if revErr == nil && !l.sessionReceipt(r, revoked) {
		revErr = ErrUnknown
	}
	if revErr == nil {
		l.revocationConfirmed = true
	}
	if l.record.binding.sid == "" {
		if revErr != nil {
			return l.failure(revErr)
		}
		if wasPlanned {
			l.state = Closed
			return nil
		}
		if l.record.stage == reserved && !l.unknown {
			if err := l.save(ctx, abortComplete, false); err != nil {
				return err
			}
			l.state = Closed
			return nil
		}
		l.state = Quarantined
		return ErrManual
	}
	if !l.plan.validBinding(l.record.binding) {
		return l.failure(ErrUnknown)
	}
	if l.deletionPending {
		if revErr != nil {
			return l.failure(revErr)
		}
		// 保存済み削除意図やaccount不在だけを、旧workloadの現在の回収
		// 証拠にしない。復旧も同じ旧bindingへのreceiptを要求する。
		if !finite(ctx) {
			return ErrCleanup
		}
		q, err := l.quiesce(ctx)
		if err != nil {
			return l.failure(err)
		}
		if !l.sessionReceipt(q, quiescent) {
			return l.failure(ErrUnknown)
		}
		return l.reconcileDeletion(ctx)
	}
	// workload回収失敗でも、同一accountへのdisabled containmentは先に行う。
	if !finite(ctx) {
		return ErrCleanup
	}
	d, disableErr := l.sources.accounts.disable(ctx, l.record.binding)
	if disableErr == nil && !l.accountReceipt(d, disabled) {
		disableErr = ErrUnknown
	}
	if revErr != nil {
		return l.failure(revErr)
	}
	if disableErr != nil {
		return l.failure(disableErr)
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	q, err := l.quiesce(ctx)
	if err != nil {
		return l.failure(err)
	}
	if !l.sessionReceipt(q, quiescent) {
		return l.failure(ErrUnknown)
	}
	if l.unknown {
		l.state = Unknown
		return ErrUnknown
	}
	if !finite(ctx) {
		return ErrCleanup
	}
	snapshot, err := l.sources.accounts.inspect(ctx, l.record.binding)
	if err != nil || !l.snapshot(snapshot, exact) || !snapshot.disabled || !finite(ctx) {
		return ErrCleanup
	}
	if l.record.stage < ownedSID {
		if err := l.save(ctx, ownedSID, false); err != nil {
			return err
		}
	}
	if err := l.save(ctx, deleteAttempt, false); err != nil {
		return err
	}
	// journal I/O中の変化を保存前snapshotで通さない。nativeのatomic
	// SID mutation保証とは別に、commit後もexact/disabledを再確認する。
	snapshot, err = l.sources.accounts.inspect(ctx, l.record.binding)
	if err != nil || !l.snapshot(snapshot, exact) || !snapshot.disabled || !finite(ctx) {
		return ErrCleanup
	}
	l.deletionPending = true
	del, err := l.sources.accounts.deleteExact(ctx, l.record.binding)
	if err != nil {
		if _, known := err.(knownFailure); known {
			l.deletionPending = false
		}
		return l.failure(err)
	}
	if !l.accountReceipt(del, deleted) {
		return l.failure(ErrUnknown)
	}
	return l.reconcileDeletion(ctx)
}

func (l *Lifecycle) reconcileDeletion(ctx context.Context) error {
	if !finite(ctx) {
		return ErrCleanup
	}
	snapshot, err := l.sources.accounts.inspect(ctx, l.record.binding)
	if err != nil || !l.snapshot(snapshot, absent) || !finite(ctx) {
		return ErrCleanup
	}
	// 不在確認後に同名accountが作られても、以後account操作へ戻らない。
	l.deletionObserved = true
	return l.finishDeletion(ctx)
}
func (l *Lifecycle) finishDeletion(ctx context.Context) error {
	if err := l.save(ctx, deleteComplete, false); err != nil {
		return err
	}
	l.state = Closed
	return nil
}
