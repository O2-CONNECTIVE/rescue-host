//go:build windows

package guestaccount

import "runtime"

// pending 操作は metadata の参照保持だけでなく実メモリを pin して残す。
// この候補は pending completion の join を実装せず、未知のまま所有する。
type nativeJournalPins struct {
	value  runtime.Pinner
	active bool
}

func (p *nativeJournalPins) hold(values ...any) {
	if p.active {
		panic("journal pending memory reused")
	}
	p.active = true
	for _, v := range values {
		p.value.Pin(v)
	}
}
func (p *nativeJournalPins) done() { p.value.Unpin(); p.active = false }
