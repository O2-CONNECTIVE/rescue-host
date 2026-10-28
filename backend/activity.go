package backend

import "sync"

// activity は close 再試行も含む現在の処理数を追跡する。
// 0 から再開した世代ごとに通知を分け、WaitGroup の再利用競合を避ける。
type activity struct {
	mu     sync.Mutex
	count  int
	signal chan struct{}
}

func newActivity() *activity { c := make(chan struct{}); close(c); return &activity{signal: c} }
func (a *activity) Add(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 {
		panic("処理数の追加は正数だけです")
	}
	if a.count == 0 {
		a.signal = make(chan struct{})
	}
	a.count += n
}
func (a *activity) Done() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count--
	if a.count < 0 {
		panic("処理数が負になりました")
	}
	if a.count == 0 {
		close(a.signal)
	}
}
func (a *activity) Signal() <-chan struct{} { a.mu.Lock(); defer a.mu.Unlock(); return a.signal }
func (a *activity) Count() int              { a.mu.Lock(); defer a.mu.Unlock(); return a.count }
