// Package lease は同一プロセス内の期限と不可逆な失効を扱う。
// 保護サービス、認証付き IPC、OS 停止中の期限執行は提供しない。
package lease

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
)

var ErrRejected = errors.New("起動同一性またはライフサイクル条件が一致しません")

type Identity struct{ LaunchID, Epoch, PolicyDigest string }

func (i Identity) Valid() bool {
	for _, s := range []string{i.LaunchID, i.Epoch} {
		if s == "" || len(s) > 128 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return false
		}
	}
	_, err := hex.DecodeString(i.PolicyDigest)
	return err == nil && len(i.PolicyDigest) == 64 && strings.ToLower(i.PolicyDigest) == i.PolicyDigest
}

type State string

const (
	Prepared   State = "prepared"
	Ready      State = "ready"
	Active     State = "active"
	Revoking   State = "revoking"
	Terminated State = "terminated"
)

type Snapshot struct {
	State           State
	Reason          string
	Pending, Active int
	Sequence        uint64
	Identity        Identity
}

type Authority struct {
	mu                        sync.Mutex
	id                        Identity
	state                     State
	reason                    string
	maximum, deadline, last   time.Time
	preparationDeadline       time.Time
	lease                     time.Duration
	capacity, pending, active int
	sequence                  uint64
	hasSequence               bool
	ctx                       context.Context
	cancel                    context.CancelFunc
	clock                     func() time.Time
	done                      chan struct{}
}

// 時計境界の読取不能は未設定時刻として閉じる。実 OS の故障再現ではない。
// clock は非公開の差替え点であり、製品は time.Now だけを使う。
func readClock(clock func() time.Time) (now time.Time) {
	if clock == nil {
		return time.Time{}
	}
	defer func() {
		if recover() != nil {
			now = time.Time{}
		}
	}()
	return clock()
}

// New は prepare 時に最大期限を固定し、ready や更新で延長しない。
func New(id Identity, maximum, lease time.Duration, capacity int) (*Authority, error) {
	return newAuthority(id, maximum, lease, capacity, time.Now)
}

func newAuthority(id Identity, maximum, lease time.Duration, capacity int, clock func() time.Time) (*Authority, error) {
	if !id.Valid() || maximum <= 0 || maximum > 2*time.Hour || lease <= 0 || lease > time.Minute || lease > maximum || capacity < 1 || capacity > 32 {
		return nil, ErrRejected
	}
	now := readClock(clock)
	if now.IsZero() {
		return nil, ErrRejected
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &Authority{preparationDeadline: now.Add(5 * time.Second), id: id, state: Prepared, maximum: now.Add(maximum), deadline: now.Add(lease), last: now, lease: lease, capacity: capacity, ctx: ctx, cancel: cancel, clock: clock, done: make(chan struct{})}
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.mu.Lock()
				a.check()
				a.mu.Unlock()
			}
		}
	}()
	return a, nil
}

func (a *Authority) revoke(reason string) {
	if a.state == Revoking || a.state == Terminated {
		return
	}
	a.state = Revoking
	a.reason = reason
	a.cancel()
}

func (a *Authority) check() {
	if a.state == Revoking || a.state == Terminated {
		return
	}
	now := readClock(a.clock)
	switch {
	case now.IsZero():
		a.revoke("clock-unavailable")
	case now.Round(0).Before(a.last.Round(0)):
		a.revoke("clock-regression")
	case !now.Before(a.maximum) || !now.Round(0).Before(a.maximum.Round(0)):
		a.revoke("maximum-deadline")
	case a.state != Active && (!now.Before(a.preparationDeadline) || !now.Round(0).Before(a.preparationDeadline.Round(0))):
		a.revoke("preparation-expired")
	case !now.Before(a.deadline) || !now.Round(0).Before(a.deadline.Round(0)):
		a.revoke("lease-expired")
	}
	a.last = now
}

func (a *Authority) Context() context.Context { return a.ctx }
func (a *Authority) Done() <-chan struct{}    { return a.done }
func (a *Authority) Identity() Identity       { return a.id }

func (a *Authority) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.check()
	return Snapshot{a.state, a.reason, a.pending, a.active, a.sequence, a.id}
}

func (a *Authority) Transition(id Identity, from, to State) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.check()
	if id != a.id || a.state != from || !((from == Prepared && to == Ready) || (from == Ready && to == Active)) {
		return ErrRejected
	}
	a.state = to
	return nil
}

func (a *Authority) IsActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.check()
	return a.state == Active
}

func (a *Authority) Renew(id Identity, sequence uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.check()
	if id != a.id || (a.state != Ready && a.state != Active) || (a.hasSequence && sequence <= a.sequence) {
		return ErrRejected
	}
	a.sequence = sequence
	a.hasSequence = true
	a.deadline = a.last.Add(a.lease)
	if a.deadline.After(a.maximum) {
		a.deadline = a.maximum
	}
	return nil
}

func (a *Authority) Revoke(id Identity) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id != a.id {
		return ErrRejected
	}
	a.revoke("explicit-stop")
	return nil
}

// Terminate は所有通信と worker の終了を確認した呼び出し元だけが使う。
func (a *Authority) Terminate() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != Revoking || a.pending != 0 || a.active != 0 {
		return ErrRejected
	}
	a.state = Terminated
	return nil
}

// Reservation は接続中から枠を占有する。Release は重複しても枠を壊さない。
type Reservation struct {
	owner              *Authority
	promoted, released bool
}

func (a *Authority) Reserve() (*Reservation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.check()
	if a.state != Active || a.pending+a.active >= a.capacity {
		return nil, ErrRejected
	}
	a.pending++
	return &Reservation{owner: a}, nil
}

func (r *Reservation) Promote() error {
	a := r.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	a.check()
	if r.released || r.promoted || a.state != Active {
		return ErrRejected
	}
	r.promoted = true
	a.pending--
	a.active++
	return nil
}

func (r *Reservation) Release() {
	a := r.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.released {
		return
	}
	r.released = true
	if r.promoted {
		a.active--
	} else {
		a.pending--
	}
}
