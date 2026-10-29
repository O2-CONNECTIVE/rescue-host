package guestaccount

// native queryの所有状態だけを読む。Goのgate/実return確認であり、nativeを
// 再照会しない。健康な通常拒否とpoison/取消/未returnを区別する。
func stagingQueryUnsafe(c *creationSource) bool {
	if c == nil || c.inspectionUnknown.Load() {
		return true
	}
	q, ok := c.deps.preflight.(*creationQueryOwner)
	if !ok {
		return false
	}
	if !q.valid() || q.stop.Load() || !q.mu.TryLock() {
		return true
	}
	defer q.mu.Unlock()
	if q.poisoned {
		return true
	}
	if q.work != nil {
		select {
		case <-q.work.done:
		default:
			return true
		}
	}
	return false
}
